package manage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func sharedSkillRemovalFixture(t *testing.T, chain bool) (*fixture, string, string) {
	t.Helper()
	f := setup(t)
	source := filepath.Join(f.home, ".agents/skills/example")
	link := filepath.Join(f.home, ".codex/skills/example")
	f.write(filepath.Join(source, "SKILL.md"), "---\nname: example\n---\n", 0644)
	addLink := func(path, target, id, owner string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		f.r.Items = append(f.r.Items, audit.Item{ID: id, Kind: "skill", Name: "example", Owner: owner, Source: "local", Path: filepath.Join(path, "SKILL.md")})
	}
	f.r.Items = []audit.Item{{ID: "a-source", Kind: "skill", Name: "example", Owner: ".agents", Source: "local", Path: filepath.Join(source, "SKILL.md")}}
	addLink(link, source, "b-inner", ".codex")
	if chain {
		outer := filepath.Join(f.home, ".claude/skills/example")
		relative, err := filepath.Rel(filepath.Dir(outer), link)
		if err != nil {
			t.Fatal(err)
		}
		addLink(outer, relative, "z-outer", ".claude")
	}
	return f, source, link
}

func skillRemovalEngine(f *fixture) Engine {
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		f.t.Fatal("Skill deletion invoked manager")
		return nil, nil
	}))
	e.Snapshot = func(context.Context) (audit.Report, error) {
		r := f.r
		r.Items = nil
		for _, i := range f.r.Items {
			if _, err := os.Stat(i.Path); err == nil {
				r.Items = append(r.Items, i)
			}
		}
		return r, nil
	}
	return e
}

func TestSharedSkillRemovalOrdersReferencesBeforeSource(t *testing.T) {
	for _, chain := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "chain"}[chain], func(t *testing.T) {
			f, _, _ := sharedSkillRemovalFixture(t, chain)
			ids := []string{"a-source", "b-inner"}
			want := []string{"b-inner", "a-source"}
			if chain {
				ids = append(ids, "z-outer")
				want = []string{"z-outer", "b-inner", "a-source"}
			}
			p := f.plan(Selection{Action: "uninstall", IDs: ids})
			got := []string{}
			for _, pi := range p.Items {
				got = append(got, pi.ID)
				if pi.Status != "ready" {
					t.Fatal(p.Items)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("unsafe dependency order", got)
			}
			if !reflect.DeepEqual(p.Selection.IDs, ids) {
				t.Fatal("selection changed", p.Selection)
			}
			out, err := Apply(context.Background(), p, p.PlanID, skillRemovalEngine(f))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range out.Results {
				if r.Status != "uninstalled" || r.BackupPath == "" {
					t.Fatal(out)
				}
			}
			for _, i := range f.r.Items {
				if _, err := os.Lstat(filepath.Dir(i.Path)); !os.IsNotExist(err) {
					t.Fatal("selected entry survived", i.ID, err)
				}
			}
		})
	}
}

func TestSharedSkillRemovalPreservesUncheckedAndProtectedReferences(t *testing.T) {
	for _, scenario := range []string{"unchecked", "system", "plugin", "outside-root", "linked-root"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, link := sharedSkillRemovalFixture(t, false)
			ids := []string{"a-source", "b-inner"}
			switch scenario {
			case "unchecked":
				ids = ids[:1]
			case "system":
				f.r.Items[1].Owner = "system"
			case "plugin":
				f.r.Items[1].Source = "plugin-cache"
			case "outside-root":
				f.o.SkillRoots = []string{filepath.Join(f.home, ".agents/skills")}
			case "linked-root":
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				root := filepath.Dir(link)
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(source), root); err != nil {
					t.Fatal(err)
				}
			}
			p := f.plan(Selection{Action: "uninstall", IDs: ids})
			for _, pi := range p.Items {
				if pi.ID == "a-source" && (pi.Status != "blocked" || pi.Reason != "skill_referenced_by_preserved_installation") {
					t.Fatal(p.Items)
				}
			}
			out, err := Apply(context.Background(), p, p.PlanID, skillRemovalEngine(f))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range out.Results {
				if r.Executed || r.Changed {
					t.Fatal(out)
				}
			}
			for _, path := range []string{source, link} {
				if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
					t.Fatal("preserved installation broken", err)
				}
			}
		})
	}
}

func TestSharedSkillRemovalFailedReferenceKeepsTarget(t *testing.T) {
	for _, scenario := range []string{"preflight-failure", "link-drift", "move-failure"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, link := sharedSkillRemovalFixture(t, false)
			p := f.plan(Selection{Action: "uninstall", IDs: []string{"a-source", "b-inner"}})
			e := skillRemovalEngine(f)
			snapshot := e.Snapshot
			calls := 0
			e.Snapshot = func(ctx context.Context) (audit.Report, error) {
				calls++
				if calls == 2 {
					if scenario == "preflight-failure" {
						return audit.Report{}, errors.New("controlled read failure")
					}
					if scenario == "move-failure" {
						f.write(filepath.Join(f.home, ".Trash"), "controlled obstacle", 0600)
						return snapshot(ctx)
					}
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					relative, err := filepath.Rel(filepath.Dir(link), source)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.Symlink(relative, link); err != nil {
						t.Fatal(err)
					}
				}
				return snapshot(ctx)
			}
			out, err := Apply(context.Background(), p, p.PlanID, e)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range out.Results {
				if scenario == "move-failure" && r.ID == "b-inner" {
					if r.Status != "failed" || !r.Executed || r.Changed {
						t.Fatal(out)
					}
				} else if r.Status != "blocked" || r.Executed || r.Changed {
					t.Fatal(out)
				}
			}
			if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
				t.Fatal("reference broken", err)
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatal("source removed", err)
			}
		})
	}
}

func TestSkillRemovalKeepsIndependentCopiesSeparate(t *testing.T) {
	f, source, link := sharedSkillRemovalFixture(t, false)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(link, "SKILL.md"), "---\nname: example\n---\n", 0644)
	p := f.plan(Selection{Action: "uninstall", IDs: []string{"a-source", "b-inner"}})
	for _, pi := range p.Items {
		if pi.Status != "ready" || pi.CanonicalID != "" {
			t.Fatal(p.Items)
		}
	}
	out, err := Apply(context.Background(), p, p.PlanID, skillRemovalEngine(f))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Results {
		if r.Status != "uninstalled" {
			t.Fatal(out)
		}
	}
	for _, path := range []string{source, link} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("copy survived", err)
		}
	}
}

func TestSharedSkillRemovalRetainsDuplicateCanonicalOutcome(t *testing.T) {
	f, _, _ := sharedSkillRemovalFixture(t, false)
	duplicate := f.r.Items[1]
	duplicate.ID = "z-duplicate"
	f.r.Items = append(f.r.Items, duplicate)
	p := f.plan(Selection{Action: "uninstall", IDs: []string{"a-source", "b-inner", "z-duplicate"}})
	out, err := Apply(context.Background(), p, p.PlanID, skillRemovalEngine(f))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Results {
		if r.ID == duplicate.ID {
			if r.Status != "skipped" || r.CanonicalID != "b-inner" || r.Reason != "duplicate_installation_target" || r.Executed {
				t.Fatal(out)
			}
		} else if r.Status != "uninstalled" {
			t.Fatal(out)
		}
	}
}

func TestSharedSkillRemovalCaseInsensitiveVolume(t *testing.T) {
	for _, scenario := range []string{"unchecked", "selected-direct", "selected-chain"} {
		t.Run(scenario, func(t *testing.T) {
			chain := scenario == "selected-chain"
			f, source, link := sharedSkillRemovalFixture(t, chain)
			aliasSource := filepath.Join(f.home, ".AGENTS/skills/example")
			sourceInfo, err := os.Lstat(source)
			if err != nil {
				t.Fatal(err)
			}
			aliasInfo, err := os.Lstat(aliasSource)
			if os.IsNotExist(err) {
				t.Skip("fixture volume is case-sensitive: .AGENTS does not resolve to .agents")
			}
			if err != nil || !os.SameFile(sourceInfo, aliasInfo) {
				t.Fatal("case alias does not identify source entry", err)
			}
			t.Log("actual fixture filesystem resolves differently cased paths to the same directory")
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(aliasSource, link); err != nil {
				t.Fatal(err)
			}
			ids := []string{"a-source"}
			want := []string{"b-inner", "a-source"}
			if scenario != "unchecked" {
				ids = append(ids, "b-inner")
			}
			if chain {
				outer := filepath.Join(f.home, ".claude/skills/example")
				if err := os.Remove(outer); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.home, ".CODEX/skills/example"), outer); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, "z-outer")
				want = []string{"z-outer", "b-inner", "a-source"}
			}
			p := f.plan(Selection{Action: "uninstall", IDs: ids})
			if scenario == "unchecked" {
				if len(p.Items) != 1 || p.Items[0].Status != "blocked" || p.Items[0].Reason != "skill_referenced_by_preserved_installation" {
					t.Fatal("source is not protected", p.Items)
				}
			} else {
				got := []string{}
				for _, pi := range p.Items {
					got = append(got, pi.ID)
					if pi.Status != "ready" {
						t.Fatal(p.Items)
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("case alias lost dependency order", got)
				}
			}
			out, err := Apply(context.Background(), p, p.PlanID, skillRemovalEngine(f))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unchecked" {
				for _, r := range out.Results {
					if r.Executed || r.Changed {
						t.Fatal("protected source mutated", out)
					}
				}
				if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
					t.Fatal("unchecked client link broken", err)
				}
			} else {
				for _, r := range out.Results {
					if r.Status != "uninstalled" {
						t.Fatal(out)
					}
				}
				for _, i := range f.r.Items {
					if _, err := os.Lstat(filepath.Dir(i.Path)); !os.IsNotExist(err) {
						t.Fatal("selected case alias survived", i.ID, err)
					}
				}
			}
		})
	}
}
