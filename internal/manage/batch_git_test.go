package manage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func gitTestArgs(c Command) []string {
	for n, arg := range c.Args {
		if arg == "-C" && n+2 < len(c.Args) {
			return c.Args[n+2:]
		}
	}
	return c.Args
}

func batchFormulaFixture(t *testing.T, affected bool) (*fixture, map[string]ResolvedTarget) {
	t.Helper()
	f := setup(t)
	root := f.o.BrewRoots[0]
	manager := filepath.Join(root, "bin/brew")
	f.write(manager, "fake brew", 0755)
	targets := map[string]ResolvedTarget{}
	for _, entry := range []struct{ id, name string }{{"a-parent", "parent"}, {"b-dependency", "dependency"}} {
		path := filepath.Join(root, "Cellar", entry.name)
		f.write(filepath.Join(path, "1.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
		i := audit.Item{ID: entry.id, Name: entry.name, Identity: entry.name, Kind: "formula", Owner: "homebrew", Source: "homebrew/core", Channel: "stable", Path: path, Version: "1.0.0", ActiveVersion: "1.0.0", Versions: []string{"1.0.0"}, Health: audit.Result{Status: "ok"}}
		f.r.Items = append(f.r.Items, i)
		targets[i.ID] = ResolvedTarget{Provider: "homebrew", ManagerPath: manager, Scope: root, Version: "2.0.0", Source: "homebrew/core:approved-dependencies", OriginVerified: true}
	}
	if affected {
		target := targets["a-parent"]
		target.AffectedPackages = []string{"dependency"}
		targets["a-parent"] = target
	}
	f.o.ResolvedTargets = targets
	return f, targets
}

func updateFormulaFixture(f *fixture, n int, version string) {
	f.t.Helper()
	f.r.Items[n].Version = version
	f.r.Items[n].ActiveVersion = version
	f.r.Items[n].Versions = []string{version}
	f.write(filepath.Join(f.r.Items[n].Path, version, "INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
}

func TestBatchDependencyAlreadySatisfiedByApprovedEarlierOperation(t *testing.T) {
	f, targets := batchFormulaFixture(t, true)
	p := f.plan(Selection{Action: "update", All: true})
	calls := 0
	e := f.engine(runFunc(func(_ context.Context, c Command) ([]byte, error) {
		calls++
		if c.Args[len(c.Args)-1] != "parent" {
			t.Fatal("dependency executed twice", c.Args)
		}
		updateFormulaFixture(f, 0, "2.0.0")
		updateFormulaFixture(f, 1, "2.0.0")
		return nil, nil
	}))
	e.Snapshot = func(context.Context) (audit.Report, error) {
		r := f.r
		r.Items = append([]audit.Item(nil), r.Items...)
		return r, nil
	}
	e.ResolveTargets = func(_ context.Context, r audit.Report, _ Selection) (map[string]ResolvedTarget, error) {
		out := map[string]ResolvedTarget{}
		for _, i := range r.Items {
			target := targets[i.ID]
			if i.Version == "2.0.0" {
				target.Source = i.Source
				target.AffectedPackages = nil
			}
			out[i.ID] = target
		}
		return out, nil
	}
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || calls != 1 || len(out.Results) != 2 || out.Results[0].Status != "updated" || out.Results[1].Status != "skipped" || out.Results[1].Reason != "already_current" || out.Results[1].Executed || out.Results[1].AfterVersion != "2.0.0" {
		t.Fatalf("approved dependency update falsely blocked: %+v, %v", out, err)
	}
}

func TestBatchSatisfactionDoesNotAcceptUnapprovedOrConcurrentChanges(t *testing.T) {
	for _, scenario := range []string{"unlisted", "outside-change", "receipt-changed", "manager-changed", "source-changed", "architecture-changed", "wrong-version", "failed-parent"} {
		t.Run(scenario, func(t *testing.T) {
			f, targets := batchFormulaFixture(t, scenario != "unlisted")
			p := f.plan(Selection{Action: "update", All: true})
			calls, snapshots := 0, 0
			e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
				calls++
				updateFormulaFixture(f, 0, "2.0.0")
				if scenario != "outside-change" {
					v := "2.0.0"
					if scenario == "wrong-version" {
						v = "3.0.0"
					}
					updateFormulaFixture(f, 1, v)
					if scenario == "architecture-changed" {
						f.r.Items[1].Architecture = []string{"unapproved-architecture"}
					}
				}
				if scenario == "failed-parent" {
					return nil, errors.New("partial manager failure")
				}
				return nil, nil
			}))
			e.Snapshot = func(context.Context) (audit.Report, error) {
				snapshots++
				if snapshots == 4 {
					switch scenario {
					case "outside-change":
						updateFormulaFixture(f, 1, "2.0.0")
					case "receipt-changed":
						f.write(filepath.Join(f.r.Items[1].Path, "2.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"},"external_change":true}`, 0644)
					case "manager-changed":
						f.write(targets["b-dependency"].ManagerPath, "externally replaced brew", 0755)
					case "source-changed":
						target := targets["b-dependency"]
						target.Source = "other-source"
						targets["b-dependency"] = target
					}
				}
				r := f.r
				r.Items = append([]audit.Item(nil), r.Items...)
				return r, nil
			}
			e.ResolveTargets = func(_ context.Context, r audit.Report, _ Selection) (map[string]ResolvedTarget, error) {
				out := map[string]ResolvedTarget{}
				for _, i := range r.Items {
					target := targets[i.ID]
					if i.Version == "2.0.0" && scenario != "source-changed" {
						target.Source = i.Source
						target.AffectedPackages = nil
					}
					out[i.ID] = target
				}
				return out, nil
			}
			out, err := Apply(context.Background(), p, p.PlanID, e)
			if err != nil || calls != 1 || out.Results[1].Status != "blocked" || out.Results[1].Executed {
				t.Fatalf("unapproved/concurrent change accepted: %+v, %v, calls=%d", out, err, calls)
			}
		})
	}
}

func TestGitSkillMaintainsOnlyVerifiedTrackingRefAcrossUpdates(t *testing.T) {
	f := setup(t)
	_, root := f.gitSkill()
	head, tracking, remote := strings.Repeat("a", 40), strings.Repeat("a", 40), strings.Repeat("b", 40)
	fetched := ""
	otherRef := strings.Repeat("d", 40)
	runner := runFunc(func(_ context.Context, c Command) ([]byte, error) {
		line := strings.Join(gitTestArgs(c), " ")
		switch {
		case strings.Contains(line, "rev-parse --verify FETCH_HEAD"):
			return []byte(fetched), nil
		case strings.Contains(line, "rev-parse --verify refs/remotes/origin/main"), strings.Contains(line, "rev-parse --verify --quiet refs/remotes/origin/main"):
			return []byte(tracking), nil
		case strings.Contains(line, "rev-parse --verify HEAD"):
			return []byte(head), nil
		case strings.Contains(line, "ls-remote"):
			return []byte(remote + "\trefs/heads/main\n"), nil
		case strings.Contains(line, "status"), strings.Contains(line, "merge-base"):
			if strings.Contains(line, "merge-base") && c.Args[len(c.Args)-2] == head && c.Args[len(c.Args)-1] == tracking && head != tracking {
				return nil, errors.New("local head ahead of stale tracking")
			}
			return nil, nil
		case strings.Contains(line, "fetch"):
			fetched = remote
			return nil, nil
		case strings.Contains(line, "update-ref"):
			args := c.Args[len(c.Args)-3:]
			if args[0] != "refs/remotes/origin/main" || args[1] != remote || args[2] != tracking {
				t.Fatal("unbound tracking mutation", c.Args)
			}
			tracking = args[1]
			return nil, nil
		case strings.Contains(line, "merge --ff-only"):
			head = c.Args[len(c.Args)-1]
			return nil, nil
		}
		t.Fatalf("unexpected Git command in %s: %v", root, c.Args)
		return nil, nil
	})
	for _, next := range []string{strings.Repeat("b", 40), strings.Repeat("c", 40)} {
		remote = next
		selection := Selection{Action: "update", All: true}
		targets, err := ResolveGitTargets(context.Background(), f.r, selection, f.o, runner)
		if err != nil || len(targets) != 1 {
			t.Fatalf("subsequent Git update unavailable: %v %v; head=%s tracking=%s", targets, err, head, tracking)
		}
		f.o.ResolvedTargets = targets
		p := f.plan(selection)
		out, err := Apply(context.Background(), p, p.PlanID, f.engine(runner))
		if err != nil || out.Results[0].Status != "updated" || tracking != next || head != next || otherRef != strings.Repeat("d", 40) {
			t.Fatalf("tracking not retained after update: %+v %v; head=%s tracking=%s", out, err, head, tracking)
		}
	}
}

func TestGitTrackingUpdateRejectsConcurrentRefAndHeadChanges(t *testing.T) {
	for _, scenario := range []string{"tracking-before-fetch", "tracking-during-fetch", "head-before-fetch", "remote-moved"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			f.gitSkill()
			original, target, external := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			resolver := runFunc(func(_ context.Context, c Command) ([]byte, error) {
				line := strings.Join(gitTestArgs(c), " ")
				if strings.Contains(line, "ls-remote") {
					return []byte(target + "\trefs/heads/main"), nil
				}
				if strings.Contains(line, "rev-parse") {
					return []byte(original), nil
				}
				return nil, nil
			})
			targets, err := ResolveGitTargets(context.Background(), f.r, Selection{Action: "update", All: true}, f.o, resolver)
			if err != nil || len(targets) != 1 {
				t.Fatal(targets, err)
			}
			f.o.ResolvedTargets = targets
			p := f.plan(Selection{Action: "update", All: true})
			head, tracking, fetched := original, original, ""
			merged, updated := false, false
			if scenario == "tracking-before-fetch" {
				tracking = external
			}
			if scenario == "head-before-fetch" {
				head = external
			}
			runner := runFunc(func(_ context.Context, c Command) ([]byte, error) {
				line := strings.Join(gitTestArgs(c), " ")
				switch {
				case strings.Contains(line, "rev-parse --verify refs/remotes/origin/main"):
					return []byte(tracking), nil
				case strings.Contains(line, "rev-parse --verify HEAD"):
					return []byte(head), nil
				case strings.Contains(line, "rev-parse --verify FETCH_HEAD"):
					return []byte(fetched), nil
				case strings.Contains(line, "fetch"):
					if scenario == "tracking-before-fetch" || scenario == "head-before-fetch" {
						t.Fatal("source changed before fetch")
					}
					fetched = target
					if scenario == "tracking-during-fetch" {
						tracking = external
					}
					if scenario == "remote-moved" {
						fetched = external
					}
					return nil, nil
				case strings.Contains(line, "update-ref"):
					if !contains(c.Args, "--no-deref") || c.Args[len(c.Args)-3] != "refs/remotes/origin/main" || c.Args[len(c.Args)-2] != target {
						t.Fatal("unexpected ref update", c.Args)
					}
					if c.Args[len(c.Args)-1] != tracking {
						return nil, errors.New("compare-and-swap rejected concurrent tracking edit")
					}
					updated = true
					tracking = target
					return nil, nil
				case strings.Contains(line, "merge-base"):
					return nil, nil
				case strings.Contains(line, "merge --ff-only"):
					merged = true
					head = target
					return nil, nil
				}
				t.Fatal("unexpected command", c.Args)
				return nil, nil
			})
			gitErr := applyGit(context.Background(), p.Items[0], normalizeOptions(f.o), runner)
			if gitErr == nil {
				t.Fatal("concurrent Git change accepted")
			}
			if merged || updated {
				t.Fatal("concurrent state overwritten", head, tracking)
			}
			if scenario == "head-before-fetch" && head != external {
				t.Fatal("local commit discarded")
			}
			if strings.HasPrefix(scenario, "tracking-") && tracking != external {
				t.Fatalf("other client's tracking edit overwritten: tracking=%s expected=%s err=%v", tracking, external, gitErr)
			}
		})
	}
}

func TestGitUpdateRechecksBranchAndSourceAfterFetch(t *testing.T) {
	for _, scenario := range []string{"checkout-same-head", "remote-changed", "head-changed", "config-changed"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			_, root := f.gitSkill()
			original, target, external := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			resolver := runFunc(func(_ context.Context, c Command) ([]byte, error) {
				args := gitTestArgs(c)
				if args[0] == "ls-remote" {
					return []byte(target + "\trefs/heads/main"), nil
				}
				if args[0] == "rev-parse" {
					return []byte(original), nil
				}
				return nil, nil
			})
			targets, err := ResolveGitTargets(context.Background(), f.r, Selection{Action: "update", All: true}, f.o, resolver)
			if err != nil || len(targets) != 1 {
				t.Fatal(targets, err)
			}
			f.o.ResolvedTargets = targets
			p := f.plan(Selection{Action: "update", All: true})
			head, fetched, tracking := original, "", original
			mutated, merged, updated := false, false, false
			runner := runFunc(func(_ context.Context, c Command) ([]byte, error) {
				args := gitTestArgs(c)
				switch args[0] {
				case "rev-parse":
					switch args[len(args)-1] {
					case "HEAD":
						return []byte(head), nil
					case "FETCH_HEAD":
						return []byte(fetched), nil
					case "refs/remotes/origin/main":
						return []byte(tracking), nil
					}
				case "fetch":
					fetched = target
					mutated = true
					switch scenario {
					case "checkout-same-head":
						f.write(filepath.Join(root, ".git/HEAD"), "ref: refs/heads/other\n", 0644)
					case "head-changed":
						head = external
					default:
						configPath := filepath.Join(root, ".git/config")
						config, err := os.ReadFile(configPath)
						if err != nil {
							t.Fatal(err)
						}
						text := string(config)
						if scenario == "remote-changed" {
							text = strings.Replace(text, "https://github.com/example/skill.git", "https://github.com/other/skill.git", 1)
						} else {
							text = strings.Replace(text, "bare = false", "bare = false\n filemode = false", 1)
						}
						f.write(configPath, text, 0644)
					}
					return nil, nil
				case "merge-base":
					return nil, nil
				case "update-ref":
					updated = true
					tracking = target
					return nil, nil
				case "merge":
					merged = true
					head = target
					return nil, nil
				}
				t.Fatal("unexpected Git command", args)
				return nil, nil
			})
			err = applyGit(context.Background(), p.Items[0], normalizeOptions(f.o), runner)
			if err == nil || !mutated || merged || updated || tracking != original {
				t.Fatalf("fetch-time %s change accepted: err=%v merged=%v updated=%v head=%s tracking=%s", scenario, err, merged, updated, head, tracking)
			}
		})
	}
}

func TestNativeGitSkillTwoUpdatesKeepTrackingWithoutTouchingOtherRefs(t *testing.T) {
	f := setup(t)
	upstream := filepath.Join(f.home, "upstream")
	root := filepath.Join(f.home, ".agents/skills/example")
	runGit := func(directory string, args ...string) string {
		t.Helper()
		prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "user.name=KeepUpdated fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "-C", directory}
		out, err := (ExecRunner{}).Run(context.Background(), Command{Path: "/usr/bin/git", Args: append(prefix, args...), Dir: f.home, Env: []string{"HOME=" + f.home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}})
		if err != nil {
			t.Fatalf("isolated native Git fixture %v failed: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	f.write(filepath.Join(upstream, "SKILL.md"), "---\nname: example\n---\nfixture revision A\n", 0644)
	runGit(upstream, "init", "--initial-branch=main")
	runGit(upstream, "add", "--", "SKILL.md")
	runGit(upstream, "commit", "--quiet", "-m", "fixture A")
	initial := runGit(upstream, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Dir(root), 0755); err != nil {
		t.Fatal(err)
	}
	runGit(f.home, "clone", "--quiet", "--", upstream, root)
	const publicOrigin = "https://github.com/example/fixture-skill.git"
	runGit(root, "remote", "set-url", "origin", publicOrigin)
	runGit(root, "update-ref", "refs/remotes/origin/unrelated", initial)
	f.r.Items = []audit.Item{{ID: "skill", Name: "example", Kind: "skill", Owner: ".agents", Source: "git", Path: filepath.Join(root, "SKILL.md"), Evidence: []string{"git-worktree-clean"}}}
	transport := runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		args := append([]string(nil), c.Args...)
		verbArgs := gitTestArgs(c)
		if len(verbArgs) > 0 && (verbArgs[0] == "fetch" || verbArgs[0] == "ls-remote") {
			found := false
			for n, arg := range args {
				if arg == publicOrigin {
					args[n] = upstream
					found = true
				}
			}
			if !found {
				t.Fatal("fixture transport received unexpected remote", c.Args)
			}
			for n, arg := range args {
				if arg == "-C" {
					v := n + 2
					tail := append([]string{"-c", "protocol.file.allow=always"}, args[v:]...)
					args = append(args[:v], tail...)
					break
				}
			}
		}
		c.Args = args
		return (ExecRunner{}).Run(ctx, c)
	})
	for _, revision := range []string{"B", "C"} {
		f.write(filepath.Join(upstream, "SKILL.md"), "---\nname: example\n---\nfixture revision "+revision+"\n", 0644)
		runGit(upstream, "add", "--", "SKILL.md")
		runGit(upstream, "commit", "--quiet", "-m", "fixture "+revision)
		target := runGit(upstream, "rev-parse", "HEAD")
		s := Selection{Action: "update", All: true}
		targets, err := ResolveGitTargets(context.Background(), f.r, s, f.o, transport)
		if err != nil || len(targets) != 1 {
			t.Fatal("native next-update resolution failed", targets, err)
		}
		f.o.ResolvedTargets = targets
		p := f.plan(s)
		engine := f.engine(transport)
		engine.ResolveTargets = func(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
			return ResolveGitTargets(ctx, r, s, f.o, transport)
		}
		out, err := Apply(context.Background(), p, p.PlanID, engine)
		if err != nil || out.Results[0].Status != "updated" {
			t.Fatalf("native fixture revision %s: %+v %v", revision, out, err)
		}
		if runGit(root, "rev-parse", "HEAD") != target || runGit(root, "rev-parse", "refs/remotes/origin/main") != target || runGit(root, "rev-parse", "refs/remotes/origin/unrelated") != initial || runGit(root, "status", "--porcelain=v1") != "" {
			t.Fatal("native tracking/other-ref/worktree verification failed")
		}
	}
}
