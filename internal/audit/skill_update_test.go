package audit

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestOwnSkillReceiptRestoresOnlyBoundInstallationSource(t *testing.T) {
	s := fixture(t)
	s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
	p, old := sourceSkill(t, s, "example")
	lockPath := filepath.Join(s.Config.Home, ".agents/.skill-lock.json")
	original := sourceReceipt(old.tree)
	writeSkillLock(t, lockPath, 3, map[string]skillReceipt{"example": original})
	s.skills()
	s.skillSources()
	if len(s.Report.Items) != 1 {
		t.Fatal(s.Report.Items)
	}
	src, e := SkillSourceForUpdate(s.Report.Items[0])
	if e != nil {
		t.Fatal(e)
	}
	write(t, p, "---\nname: example\n---\nupdated\n")
	newSnap, e := ReadSkillUpdateSnapshot(filepath.Dir(p))
	if e != nil {
		t.Fatal(e)
	}
	src.Baseline, src.HashKind = newSnap.Tree, "tree"
	receipt := SkillUpdateReceipt{Schema: SkillUpdateReceiptSchema, Source: src, Commit: strings.Repeat("c", 40), Tree: newSnap.Tree}
	b, _ := json.Marshal(receipt)
	ownPath := SkillReceiptPath(s.Config.Home, src.Root)
	if e = os.MkdirAll(filepath.Dir(ownPath), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(ownPath, b, 0600); e != nil {
		t.Fatal(e)
	}
	fresh := New(s.Config)
	fresh.skills()
	fresh.skillSources()
	got, e := SkillSourceForUpdate(fresh.Report.Items[0])
	if e != nil || got.Baseline != newSnap.Tree || got.ReceiptDigest == "" {
		t.Fatalf("%#v %v", got, e)
	}
	changed := original
	changed.Source = "other/skills"
	changed.SourceURL = "https://github.com/other/skills"
	writeSkillLock(t, lockPath, 3, map[string]skillReceipt{"example": changed})
	conflict := New(s.Config)
	conflict.skills()
	conflict.skillSources()
	if conflict.Report.Items[0].Update.Code != "skill_source_conflict" || conflict.Report.Items[0].skillSource != nil {
		t.Fatal("new external source was silently replaced")
	}
}
func TestSkillUpdateSnapshotRejectsUnhashedFiles(t *testing.T) {
	s := fixture(t)
	s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
	p, _ := sourceSkill(t, s, "example")
	if e := os.Symlink("SKILL.md", filepath.Join(filepath.Dir(p), "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadSkillUpdateSnapshot(filepath.Dir(p)); e == nil {
		t.Fatal("link accepted")
	}
}

func TestOwnSkillSourceCannotChangeOriginWhileKeepingReceiptDigest(t *testing.T) {
	s := fixture(t)
	s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
	_, snap := sourceSkill(t, s, "example")
	lock := filepath.Join(s.Config.Home, ".agents/.skill-lock.json")
	writeSkillLock(t, lock, 3, map[string]skillReceipt{"example": sourceReceipt(snap.tree)})
	s.skills()
	s.skillSources()
	source, e := SkillSourceForUpdate(s.Report.Items[0])
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifySkillUpdateSource(source, s.Config.Home); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"repo", "ref", "entry", "record_path"} {
		t.Run(field, func(t *testing.T) {
			changed := source
			switch field {
			case "repo":
				changed.Repo = "different/project"
			case "ref":
				changed.Ref = "different-branch"
			case "entry":
				changed.Entry = "another/SKILL.md"
			case "record_path":
				b, _ := os.ReadFile(lock)
				changed.SourcePath = filepath.Join(s.Config.Home, "arbitrary.json")
				write(t, changed.SourcePath, string(b))
			}
			if VerifySkillUpdateSource(changed, s.Config.Home) == nil {
				t.Fatal("changed authority was accepted")
			}
		})
	}
}
func TestSkillUpdateDownloadVerifiesGitObjects(t *testing.T) {
	s := fixture(t)
	s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
	p, snap := sourceSkill(t, s, "example")
	src := SkillUpdateSource{Repo: "example/skills", Entry: "SKILL.md", Root: filepath.Dir(p), Baseline: snap.tree, HashKind: "tree", SourcePath: filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), SourceName: "example", SourceKind: "tree", SourceDigest: strings.Repeat("a", 64)}
	commit := strings.Repeat("c", 40)
	for _, wrong := range []string{"", "blob", "tree", "commit"} {
		t.Run(wrong, func(t *testing.T) {
			client := &http.Client{Transport: transport(func(req *http.Request) (*http.Response, error) {
				var v any
				switch {
				case strings.Contains(req.URL.Path, "/commits/"):
					sha := commit
					if wrong == "commit" {
						sha = strings.Repeat("d", 40)
					}
					v = map[string]any{"sha": sha, "commit": map[string]any{"tree": map[string]string{"sha": snap.tree}}}
				case strings.Contains(req.URL.Path, "/git/trees/"):
					tree := treeForSnapshot(snap)
					if wrong == "tree" {
						tree.Tree[0].Path = "changed.md"
					}
					v = tree
				case strings.Contains(req.URL.Path, "/git/blobs/"):
					data := snap.files[0].data
					if wrong == "blob" {
						data = []byte("wrong")
					}
					v = map[string]any{"sha": snap.files[0].hash, "size": len(data), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data)}
				}
				b, _ := json.Marshal(v)
				return response(200, string(b)), nil
			})}
			got, e := DownloadSkillUpdate(context.Background(), src, commit, snap.tree, client)
			if wrong == "" {
				if e != nil || got.Tree != snap.tree || len(got.Files) != 1 {
					t.Fatal(got, e)
				}
			} else if e == nil {
				t.Fatal("unverified objects accepted")
			}
		})
	}
}

func updateTestTree(entries []skillTreeEntry) skillTree {
	sort.Slice(entries, func(a, b int) bool {
		x, y := entries[a].Path, entries[b].Path
		if entries[a].Type == "tree" {
			x += "/"
		}
		if entries[b].Type == "tree" {
			y += "/"
		}
		return x < y
	})
	var data []byte
	for _, entry := range entries {
		mode := entry.Mode
		if mode == "040000" {
			mode = "40000"
		}
		raw, _ := hex.DecodeString(entry.SHA)
		data = append(data, []byte(mode+" "+entry.Path+"\x00")...)
		data = append(data, raw...)
	}
	return skillTree{SHA: skillObject("tree", data), Tree: entries}
}
func TestSkillUpdateAllowsUnsupportedFilesOnlyOutsideSelectedSkill(t *testing.T) {
	for _, inside := range []bool{false, true} {
		t.Run(fmt.Sprint(inside), func(t *testing.T) {
			s := fixture(t)
			s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
			p, snap := sourceSkill(t, s, "example")
			selected := treeForSnapshot(snap)
			if inside {
				selected = updateTestTree(append(selected.Tree, skillTreeEntry{Path: "link", Type: "blob", Mode: "120000", SHA: strings.Repeat("e", 40)}))
			}
			skills := updateTestTree([]skillTreeEntry{{Path: "example", Type: "tree", Mode: "040000", SHA: selected.SHA}})
			root := updateTestTree([]skillTreeEntry{{Path: "skills", Type: "tree", Mode: "040000", SHA: skills.SHA}, {Path: "outside-link", Type: "blob", Mode: "120000", SHA: strings.Repeat("e", 40)}, {Path: "outside-module", Type: "commit", Mode: "160000", SHA: strings.Repeat("d", 40)}})
			trees := map[string]skillTree{root.SHA: root, skills.SHA: skills, selected.SHA: selected}
			src := SkillUpdateSource{Repo: "example/skills", Ref: "main", Entry: "skills/example/SKILL.md", Root: filepath.Dir(p), Baseline: snap.tree, HashKind: "tree", SourcePath: filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), SourceName: "example", SourceKind: "tree", SourceDigest: strings.Repeat("a", 64)}
			commit := strings.Repeat("c", 40)
			client := &http.Client{Transport: transport(func(req *http.Request) (*http.Response, error) {
				var v any
				switch {
				case strings.Contains(req.URL.Path, "/commits/"):
					if filepath.Base(req.URL.Path) != "main" {
						t.Fatal("receipt ref was not independently resolved")
					}
					v = map[string]any{"sha": commit, "commit": map[string]any{"tree": map[string]string{"sha": root.SHA}}}
				case strings.Contains(req.URL.Path, "/git/trees/"):
					tree, ok := trees[filepath.Base(req.URL.Path)]
					if !ok {
						t.Fatal("requested outside tree")
					}
					v = tree
				case strings.Contains(req.URL.Path, "/git/blobs/"):
					if filepath.Base(req.URL.Path) != snap.files[0].hash {
						t.Fatal("requested outside or unsupported blob")
					}
					data := snap.files[0].data
					v = map[string]any{"sha": snap.files[0].hash, "size": len(data), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data)}
				default:
					t.Fatal("unexpected request")
				}
				b, _ := json.Marshal(v)
				return response(200, string(b)), nil
			})}
			got, e := DownloadSkillUpdate(context.Background(), src, "", "", client)
			if inside {
				if e == nil || e.Error() != "skill_tree_unsupported_file" {
					t.Fatalf("selected symlink accepted %v", e)
				}
			} else if e != nil || got.Commit != commit || got.Tree != selected.SHA || len(got.Files) != 1 {
				t.Fatalf("outside files blocked valid skill %#v %v", got, e)
			}
		})
	}
}

func TestSkillPinChangeInvalidatesPreviewAndOwnReceipt(t *testing.T) {
	for _, pin := range []string{`"v1"`, `"v2"`, `true`, `null`} {
		t.Run(pin, func(t *testing.T) {
			s := fixture(t)
			s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
			_, snap := sourceSkill(t, s, "example")
			r := sourceReceipt(snap.tree)
			lock := filepath.Join(s.Config.Home, ".agents/.skill-lock.json")
			writeSkillLock(t, lock, 3, map[string]skillReceipt{"example": r})
			s.skills()
			s.skillSources()
			src, err := SkillSourceForUpdate(s.Report.Items[0])
			if err != nil {
				t.Fatal(err)
			}
			if err = VerifySkillUpdateSource(src, s.Config.Home); err != nil {
				t.Fatal(err)
			}
			ownPath := SkillReceiptPath(s.Config.Home, src.Root)
			b, err := json.Marshal(SkillUpdateReceipt{Schema: SkillUpdateReceiptSchema, Source: src, Commit: strings.Repeat("c", 40), Tree: snap.tree})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(ownPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(ownPath, b, 0600); err != nil {
				t.Fatal(err)
			}
			r.PinnedRef = json.RawMessage(pin)
			writeSkillLock(t, lock, 3, map[string]skillReceipt{"example": r})
			if err = VerifySkillUpdateSource(src, s.Config.Home); err == nil || err.Error() != "skill_source_receipt_changed" {
				t.Fatal("old source accepted", err)
			}
			// Even a source carrying the new digest must not authorize a pin.
			changed := src
			changed.SourceDigest = sourceRecordDigest(r, "tree")
			if err = VerifySkillUpdateSource(changed, s.Config.Home); err == nil {
				t.Fatal("pinned source accepted")
			}
			fresh := New(s.Config)
			fresh.skills()
			fresh.skillSources()
			if len(fresh.Report.Items) != 1 || fresh.Report.Items[0].skillSource != nil || fresh.Report.Items[0].Update.Code != "skill_source_unverified" {
				t.Fatal("own receipt restored pinned source", fresh.Report.Items)
			}
			missing := src
			missing.SourceDigest = ""
			if VerifySkillUpdateSource(missing, s.Config.Home) == nil {
				t.Fatal("missing evidence accepted")
			}
		})
	}
}
