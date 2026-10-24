package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func selectorFixture(t *testing.T, owner, selector string) (Resolver, audit.Item, manage.Binding, *[]manage.Command) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := filepath.Join(home, "prefix")
	root := filepath.Join(scope, "lib/node_modules")
	if owner == "bun" {
		scope = filepath.Join(home, ".bun/install/global")
		root = filepath.Join(scope, "node_modules")
	}
	if owner == "pnpm" {
		scope = filepath.Join(home, "Library/pnpm/global/5")
		root = filepath.Join(scope, "node_modules")
	}
	path := filepath.Join(root, "fixture")
	actual := path
	if owner == "pnpm" {
		actual = filepath.Join(scope, ".pnpm/fixture@1.0.0/node_modules/fixture")
	}
	write := func(path, data string, mode os.FileMode) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, []byte(data), mode); e != nil {
			t.Fatal(e)
		}
	}
	write(filepath.Join(actual, "package.json"), `{"name":"fixture","version":"1.0.0"}`, 0644)
	if owner == "pnpm" {
		write(filepath.Join(root, ".modules.yaml"), "layoutVersion: 5\nnodeLinker: isolated\npackageManager: pnpm@10.18.3\nvirtualStoreDir: ../.pnpm\n", 0644)
		if e := os.Symlink(actual, path); e != nil {
			t.Fatal(e)
		}
	}
	if owner != "npm" || selector != "default" {
		manifest := filepath.Join(scope, "package.json")
		if owner == "npm" {
			manifest = filepath.Join(scope, "lib/package.json")
		}
		data, _ := json.Marshal(map[string]any{"dependencies": map[string]string{"fixture": selector}})
		write(manifest, string(data), 0644)
	}
	manager := filepath.Join(home, "bin", owner)
	write(manager, "controlled manager", 0755)
	item := audit.Item{ID: "fixture", Kind: "node-package", Name: "fixture", Identity: "fixture", Owner: owner, Version: "1.0.0", Path: path, Source: "npm-public-candidate", Health: audit.Result{Status: "ok"}}
	r := Resolver{Options: manage.Options{Home: home, ManagerPaths: map[string]string{owner: manager}}}
	b, err := manage.ResolutionBinding(item, audit.Report{Items: []audit.Item{item}}, r.Options)
	if err != nil {
		t.Fatal(err)
	}
	calls := []manage.Command{}
	r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
		calls = append(calls, c)
		if c.Path != manager {
			t.Fatal("wrong owner", c)
		}
		switch c.Args[0] {
		case "--version":
			return []byte(map[string]string{"npm": "11.19.1", "pnpm": "10.18.3", "bun": "1.4.3"}[owner]), nil
		case "pm":
			return []byte(manage.NodeBin("bun", b, r.Options)), nil
		case "root":
			return []byte(root), nil
		case "ls":
			return []byte(`{"dependencies":{"fixture":{"version":"1.0.0"}}}`), nil
		case "list":
			return []byte(`[{"dependencies":{"fixture":{"version":"1.0.0"}}}]`), nil
		case "config":
			if c.Args[2] == "tag" {
				return []byte("latest"), nil
			}
			if owner == "pnpm" && !strings.Contains(strings.Join(c.Args, "|"), manage.PnpmDisableRuntime) {
				t.Fatal("mutation permits runtime provisioning", c.Args)
			}
			if owner == "pnpm" && (!strings.Contains(strings.Join(c.Args, "|"), "--dir|"+scope) || !strings.Contains(strings.Join(c.Args, "|"), "--config.virtual-store-dir=.pnpm")) {
				t.Fatal("query configuration differs from update", c.Args)
			}
			if c.Args[2] == "registry" {
				return []byte("https://registry.example.invalid/"), nil
			}
			return []byte("undefined"), nil
		case "view", "info":
			v := "1.1.0"
			if c.Args[1] == "fixture@latest" {
				v = "2.0.0"
			}
			if c.Args[1] == "fixture@1.0.0" {
				v = "1.0.0"
			}
			return json.Marshal(map[string]any{"name": "fixture", "version": v, "optionalDependencies": map[string]string{"child": "1.0.0"}, "dist": map[string]string{"tarball": "https://registry.example.invalid/fixture.tgz"}})
		default:
			t.Fatal("preview performed a mutation", c)
		}
		return nil, nil
	})
	return r, item, b, &calls
}

func TestNodeSelectorsChooseAllowedVersionAndBindEvidence(t *testing.T) {
	for _, owner := range []string{"npm", "pnpm", "bun"} {
		for _, selector := range []string{"latest", "*", "", "^1.0.0", "~1.0.0", "1.0.0"} {
			t.Run(owner+"/"+selector, func(t *testing.T) {
				r, i, b, _ := selectorFixture(t, owner, selector)
				// ~1.0.0 must use a patch candidate, even if latest is a major ahead.
				base := r.Runner
				r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
					if (c.Args[0] == "view" || c.Args[0] == "info") && c.Args[1] == "fixture@~1.0.0" {
						return []byte(`{"name":"fixture","version":"1.0.2"}`), nil
					}
					return base.Run(context.Background(), c)
				})
				var target manage.ResolvedTarget
				var err error
				if owner == "bun" {
					target, err = r.bun(context.Background(), i, b)
				} else {
					target, err = r.node(context.Background(), i, b)
				}
				want := "1.1.0"
				if selector == "latest" || selector == "*" || selector == "" {
					want = "2.0.0"
				}
				if selector == "~1.0.0" {
					want = "1.0.2"
				}
				if selector == "1.0.0" {
					want = "1.0.0"
				}
				if err != nil || target.Version != want || target.Node == nil || target.Node.Selection.Selector != selector || target.Node.LatestVersion != "2.0.0" || target.Node.ManagerFingerprint != b.ManagerFingerprint {
					t.Fatal(target, err)
				}
				target.Provider, target.Scope, target.ManagerPath = owner, b.Scope, b.ManagerPath
				r.Options.ResolvedTargets = map[string]manage.ResolvedTarget{i.ID: target}
				p, e := manage.NewPlan(audit.Report{Items: []audit.Item{i}}, manage.Selection{Action: "update", All: true, Special: true}, r.Options)
				status := "ready"
				if selector == "1.0.0" {
					status = "skipped"
				}
				if e != nil || p.Items[0].Status != status {
					t.Fatal(p.Items, e)
				}
			})
		}
	}
}

func TestNodeQueryRejectsUnknownCapabilitiesAndSelectorsWithoutMutation(t *testing.T) {
	for _, owner := range []string{"npm", "pnpm", "bun"} {
		for _, scenario := range []string{"manager", "selector", "pin", "outside-range", "root", "layout", "registry"} {
			if owner != "pnpm" && scenario == "layout" {
				continue
			}
			t.Run(owner+"/"+scenario, func(t *testing.T) {
				selector := "^1.0.0"
				if scenario == "selector" {
					selector = "git+https://user:SECRET@private/repo"
				}
				if scenario == "pin" {
					selector = "1.0.0"
				}
				r, i, b, _ := selectorFixture(t, owner, selector)
				if owner == "bun" && scenario == "root" {
					b.RealPath = filepath.Join(r.Options.Home, "wrong-root")
				}
				if owner == "bun" && scenario == "registry" {
					if err := os.WriteFile(filepath.Join(i.Path, "package.json"), []byte(`{"name":"fixture","version":"1.0.0","_resolved":"https://different.invalid/fixture.tgz"}`), 0644); err != nil {
						t.Fatal(err)
					}
				}
				base := r.Runner
				if scenario == "layout" {
					if e := os.WriteFile(filepath.Join(b.Scope, "node_modules/.modules.yaml"), []byte("layoutVersion: 99\n"), 0644); e != nil {
						t.Fatal(e)
					}
				}
				r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
					if scenario == "manager" && c.Args[0] == "--version" {
						return []byte("99.0.0"), nil
					}
					if scenario == "root" && c.Args[0] == "root" {
						return []byte(filepath.Join(r.Options.Home, "wrong-root")), nil
					}
					if scenario == "registry" && c.Args[0] == "config" && c.Args[2] == "registry" {
						return []byte("https://user:SECRET@private.invalid"), nil
					}
					if (scenario == "pin" || scenario == "outside-range") && (c.Args[0] == "view" || c.Args[0] == "info") {
						return []byte(`{"name":"fixture","version":"2.0.0"}`), nil
					}
					return base.Run(context.Background(), c)
				})
				var target manage.ResolvedTarget
				var err error
				if owner == "bun" {
					target, err = r.bun(context.Background(), i, b)
				} else {
					target, err = r.node(context.Background(), i, b)
				}
				if err == nil || target.OriginVerified {
					t.Fatal("unsupported query accepted", target)
				}
				data, _ := json.Marshal(target)
				if strings.Contains(string(data), "SECRET") {
					t.Fatal("private selector leaked")
				}
			})
		}
	}
}

func TestNodeRangeQueryChoosesHighestNativeResultAndLatestIsOnlyAHint(t *testing.T) {
	r, i, b, _ := selectorFixture(t, "npm", "^1.0.0")
	base := r.Runner
	r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
		if c.Args[0] == "view" {
			if c.Args[1] == "fixture@latest" {
				return nil, errors.New("separate latest failed")
			}
			return []byte(`[{"name":"fixture","version":"1.1.0"},{"name":"fixture","version":"1.0.0"},{"name":"fixture","version":"1.2.0"}]`), nil
		}
		return base.Run(context.Background(), c)
	})
	target, err := r.node(context.Background(), i, b)
	if err != nil || target.Version != "1.2.0" || target.Node.LatestVersion != "" {
		t.Fatal(target, err)
	}
}

func TestNodeFreshResolutionRejectsConfigManifestManagerDrift(t *testing.T) {
	for _, owner := range []string{"npm", "pnpm", "bun"} {
		for _, change := range []string{"config", "manifest", "manager", "version"} {
			t.Run(owner+"/"+change, func(t *testing.T) {
				r, i, b, _ := selectorFixture(t, owner, "^1.0.0")
				report := audit.Report{Items: []audit.Item{i}}
				selection := manage.Selection{Action: "update", All: true, Special: true}
				targets, err := r.Resolve(context.Background(), report, selection)
				if err != nil {
					t.Fatal(err)
				}
				o := r.Options
				o.ResolvedTargets = targets
				p, err := manage.NewPlan(report, selection, o)
				if err != nil || p.Items[0].Status != "ready" {
					t.Fatal(p.Items, err)
				}
				switch change {
				case "config":
					if err := os.WriteFile(filepath.Join(r.Options.Home, ".npmrc"), []byte("//private.invalid/:_authToken=SECRET\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "manifest":
					path := filepath.Join(b.Scope, "package.json")
					if owner == "npm" {
						path = filepath.Join(b.Scope, "lib/package.json")
					}
					if err := os.WriteFile(path, []byte(`{"dependencies":{"fixture":"~1.0.0"}}`), 0644); err != nil {
						t.Fatal(err)
					}
				case "manager":
					if err := os.WriteFile(b.ManagerPath, []byte("changed manager content"), 0755); err != nil {
						t.Fatal(err)
					}
				case "version":
					base := r.Runner
					r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
						if c.Args[0] == "--version" {
							return []byte("99.0.0"), nil
						}
						return base.Run(context.Background(), c)
					})
				}
				mutations := 0
				engine := manage.Engine{Options: r.Options, StateDir: filepath.Join(r.Options.Home, "state"), Runner: queryRunner(func(manage.Command) ([]byte, error) { mutations++; return nil, nil }), Snapshot: func(context.Context) (audit.Report, error) { return report, nil }, ResolveTargets: r.Resolve}
				_, err = manage.Apply(context.Background(), p, p.PlanID, engine)
				if err == nil || mutations != 0 {
					t.Fatalf("drift accepted: %v mutations=%d", err, mutations)
				}
			})
		}
	}
}

func TestNpmDefaultPolicyRequiresNativeLatestTag(t *testing.T) {
	r, i, b, _ := selectorFixture(t, "npm", "default")
	base := r.Runner
	r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
		if c.Args[0] == "config" && c.Args[2] == "tag" {
			return []byte("next"), nil
		}
		return base.Run(context.Background(), c)
	})
	if target, err := r.node(context.Background(), i, b); err == nil || target.OriginVerified {
		t.Fatal("custom tag silently replaced by latest", target, err)
	}
}

func TestNodeApprovedExactUpdatePreservesSelectorThroughReadback(t *testing.T) {
	for _, owner := range []string{"npm", "pnpm", "bun"} {
		t.Run(owner, func(t *testing.T) {
			selector := "^1.0.0"
			want := "1.1.0"
			if owner == "npm" {
				selector = "default"
				want = "2.0.0"
			}
			r, i, b, _ := selectorFixture(t, owner, selector)
			report := audit.Report{Items: []audit.Item{i}}
			selection := manage.Selection{Action: "update", All: true, Special: true}
			targets, err := r.Resolve(context.Background(), report, selection)
			if err != nil {
				t.Fatal(err)
			}
			o := r.Options
			o.ResolvedTargets = targets
			p, err := manage.NewPlan(report, selection, o)
			if err != nil || p.Items[0].Status != "ready" {
				t.Fatal(p.Items, err)
			}
			before, err := audit.ReadNodeSelector(owner, i.Name, i.Path, b.Scope)
			if err != nil {
				t.Fatal(err)
			}
			mutations := 0
			engine := manage.Engine{Options: r.Options, StateDir: filepath.Join(r.Options.Home, "state"), Snapshot: func(context.Context) (audit.Report, error) { return report, nil }, ResolveTargets: r.Resolve, Runner: queryRunner(func(c manage.Command) ([]byte, error) {
				mutations++
				if c.Path != b.ManagerPath || !strings.Contains(strings.Join(c.Args, "|"), "--no-save|") || c.Args[len(c.Args)-1] != "fixture@"+want {
					t.Fatal("not exact/no-save", c)
				}
				if owner == "pnpm" && (!strings.Contains(strings.Join(c.Args, "|"), "--dir|"+b.Scope) || !strings.Contains(strings.Join(c.Args, "|"), "--config.bin="+targets[i.ID].Node.Bin)) {
					t.Fatal("pnpm scope/bin lost", c)
				}
				if owner == "pnpm" {
					target := filepath.Join(b.Scope, ".pnpm", "fixture@"+want, "node_modules/fixture")
					if err := os.MkdirAll(target, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(i.Path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, i.Path); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(i.Path, "package.json"), []byte(`{"name":"fixture","version":"`+want+`"}`), 0644); err != nil {
					t.Fatal(err)
				}
				report.Items[0].Version = want
				return nil, nil
			})}
			result, err := manage.Apply(context.Background(), p, p.PlanID, engine)
			if err != nil || len(result.Results) != 1 || result.Results[0].Status != "updated" || mutations != 1 {
				t.Fatal(result, err, mutations)
			}
			after, e := audit.ReadNodeSelector(owner, i.Name, i.Path, b.Scope)
			if e != nil || after != before {
				t.Fatal("selector changed", after, e)
			}
			if _, err := manage.Apply(context.Background(), p, p.PlanID, engine); err == nil || mutations != 1 {
				t.Fatal("attempt replayed", err, mutations)
			}
		})
	}
}

func TestPnpmRuntimeSelectionCannotTurnQueriesIntoRuntimeInstalls(t *testing.T) {
	r, i, b, _ := selectorFixture(t, "pnpm", "^1.0.0")
	if err := os.WriteFile(filepath.Join(r.Options.Home, ".npmrc"), []byte("use-node-version=99.0.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := r.Runner
	r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
		if !strings.Contains(strings.Join(c.Args, "|"), manage.PnpmDisableRuntime) {
			t.Fatal("runtime provisioning not suppressed", c)
		}
		return base.Run(context.Background(), c)
	})
	target, err := r.node(context.Background(), i, b)
	if err == nil || target.OriginVerified || failureReason(err) != "node_runtime_selection_requires_review" {
		t.Fatal(target, err)
	}
}

func TestPnpmNativeRCWithRuntimeSelectionIsManual(t *testing.T) {
	r, i, _, _ := selectorFixture(t, "pnpm", "^1.0.0")
	rc := filepath.Join(r.Options.Home, "Library/Preferences/pnpm/rc")
	if err := os.MkdirAll(filepath.Dir(rc), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rc, []byte("use-node-version=99.0.0\n//private.invalid/:_authToken=PRIVATE_RC_TOKEN\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report := audit.Report{Items: []audit.Item{i}}
	selection := manage.Selection{Action: "update", All: true, Special: true}
	targets, err := r.Resolve(context.Background(), report, selection)
	if err != nil {
		t.Fatal(err)
	}
	options := r.Options
	options.ResolvedTargets = targets
	plan, err := manage.NewPlan(report, selection, options)
	if err != nil || len(plan.Items) != 1 || plan.Items[0].Status != "manual" || plan.Items[0].Reason != "node_runtime_selection_requires_review" {
		t.Fatalf("native rc runtime selection executable: %+v %v", plan.Items, err)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "PRIVATE_RC_TOKEN") {
		t.Fatal("native rc credential leaked")
	}
}

func TestPnpmNativeRCDriftRejectsApplyBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"create-runtime", "change-runtime", "create-store", "change-store", "remove-rc"} {
		t.Run(scenario, func(t *testing.T) {
			r, i, _, _ := selectorFixture(t, "pnpm", "^1.0.0")
			rc := filepath.Join(r.Options.Home, "Library/Preferences/pnpm/rc")
			if err := os.MkdirAll(filepath.Dir(rc), 0755); err != nil {
				t.Fatal(err)
			}
			original := "store-dir=/original/store\n//private.invalid/:_authToken=PRIVATE_RC_TOKEN\n"
			if strings.HasPrefix(scenario, "change-") || scenario == "remove-rc" {
				if err := os.WriteFile(rc, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report := audit.Report{Items: []audit.Item{i}}
			selection := manage.Selection{Action: "update", All: true, Special: true}
			targets, err := r.Resolve(context.Background(), report, selection)
			if err != nil {
				t.Fatal(err)
			}
			options := r.Options
			options.ResolvedTargets = targets
			plan, err := manage.NewPlan(report, selection, options)
			if err != nil || plan.Items[0].Status != "ready" {
				t.Fatal(plan.Items, err)
			}
			encoded, _ := json.Marshal(plan)
			if strings.Contains(string(encoded), "PRIVATE_RC_TOKEN") {
				t.Fatal("native rc credential leaked")
			}
			changed := "store-dir=/changed/store\n"
			if strings.HasSuffix(scenario, "runtime") {
				changed = "use-node-version=99.0.0\n"
			}
			if scenario == "remove-rc" {
				err = os.Remove(rc)
			} else {
				err = os.WriteFile(rc, []byte(changed), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			mutations := 0
			engine := manage.Engine{Options: r.Options, StateDir: filepath.Join(r.Options.Home, "state"), Snapshot: func(context.Context) (audit.Report, error) { return report, nil }, ResolveTargets: r.Resolve, Runner: queryRunner(func(manage.Command) ([]byte, error) { mutations++; return nil, errors.New("controlled stop") })}
			_, err = manage.Apply(context.Background(), plan, plan.PlanID, engine)
			if err == nil || mutations != 0 {
				t.Fatalf("native rc drift reached mutation: err=%v mutations=%d", err, mutations)
			}
		})
	}
}
