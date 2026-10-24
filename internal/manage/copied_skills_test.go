package manage

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type skillTransport func(*http.Request) (*http.Response, error)

func (f skillTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type copiedFixture struct {
	projects                               []string
	requests                               []string
	t                                      *testing.T
	home, root, remote, lock, tree, commit string
	o                                      Options
	trees                                  map[string]any
	blobs                                  map[string][]byte
	badBlob                                bool
}

func skillTestObject(kind string, b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d%c", kind, len(b), 0)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (f *copiedFixture) write(root, name, body string, mode os.FileMode) {
	f.t.Helper()
	p := filepath.Join(root, name)
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		f.t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), mode); e != nil {
		f.t.Fatal(e)
	}
}
func copiedSetup(t *testing.T) *copiedFixture {
	t.Helper()
	home, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	f := &copiedFixture{t: t, home: home, root: filepath.Join(home, ".agents/skills/example"), remote: filepath.Join(home, "upstream"), lock: filepath.Join(home, ".agents/.skill-lock.json"), commit: strings.Repeat("c", 40), trees: map[string]any{}, blobs: map[string][]byte{}}
	f.o = normalizeOptions(Options{Home: home, Now: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), BrewRoots: []string{}, AppRoots: []string{}})
	f.write(f.root, "SKILL.md", "---\nname: example\n---\nold\n", 0644)
	f.write(f.root, "deleted.txt", "removed\n", 0644)
	f.write(f.remote, "SKILL.md", "---\nname: example\n---\nnew\n", 0644)
	f.write(f.remote, "scripts/run.sh", "echo new\n", 0755)
	snap, e := audit.ReadSkillUpdateSnapshot(f.root)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(map[string]any{"version": 3, "skills": map[string]any{"example": map[string]any{"source": "example/skills", "sourceType": "github", "sourceUrl": "https://github.com/example/skills", "skillPath": "SKILL.md", "skillFolderHash": snap.Tree}}})
	f.write(filepath.Dir(f.lock), filepath.Base(f.lock), string(b), 0644)
	f.tree = f.remoteTree(f.remote)
	f.o.SkillHTTPClient = &http.Client{Transport: skillTransport(f.serve)}
	return f
}
func (f *copiedFixture) remoteTree(dir string) string {
	f.t.Helper()
	es, e := os.ReadDir(dir)
	if e != nil {
		f.t.Fatal(e)
	}
	type entry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	entries := []entry{}
	for _, de := range es {
		p := filepath.Join(dir, de.Name())
		x := entry{Path: de.Name(), Mode: "100644", Type: "blob"}
		if de.IsDir() {
			x.Type, x.Mode, x.SHA = "tree", "040000", f.remoteTree(p)
		} else {
			data, e := os.ReadFile(p)
			if e != nil {
				f.t.Fatal(e)
			}
			x.SHA = skillTestObject("blob", data)
			f.blobs[x.SHA] = data
			st, _ := os.Stat(p)
			if st.Mode().Perm()&0111 != 0 {
				x.Mode = "100755"
			}
		}
		entries = append(entries, x)
	}
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
	for _, x := range entries {
		mode := x.Mode
		if mode == "040000" {
			mode = "40000"
		}
		raw, _ := hex.DecodeString(x.SHA)
		data = append(data, []byte(mode+" "+x.Path+"\x00")...)
		data = append(data, raw...)
	}
	sha := skillTestObject("tree", data)
	f.trees[sha] = map[string]any{"sha": sha, "tree": entries, "truncated": false}
	return sha
}
func (f *copiedFixture) serve(r *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, r.URL.Path)
	if r.Method != "GET" || r.URL.Host != "api.github.com" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
		f.t.Fatalf("unsafe request %v", r)
	}
	var v any
	sha := filepath.Base(r.URL.Path)
	switch {
	case strings.Contains(r.URL.Path, "/commits/"):
		v = map[string]any{"sha": f.commit, "commit": map[string]any{"tree": map[string]string{"sha": f.tree}}}
	case strings.Contains(r.URL.Path, "/git/trees/"):
		v = f.trees[sha]
	case strings.Contains(r.URL.Path, "/git/blobs/"):
		data := f.blobs[sha]
		if f.badBlob {
			data = []byte("wrong bytes")
		}
		v = map[string]any{"sha": sha, "size": len(data), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data)}
	default:
		f.t.Fatalf("unexpected request %v", r)
	}
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}, nil
}
func (f *copiedFixture) report() audit.Report {
	f.t.Helper()
	s := audit.New(audit.Config{Home: f.home, AppRoots: []string{}, BrewRoots: []string{}, SkillRoots: f.o.SkillRoots, Projects: f.projects, Timeout: time.Second})
	r := s.Scan(true)
	items := []audit.Item{}
	for _, i := range r.Items {
		if i.Kind != "skill" {
			continue
		}
		items = append(items, i)
	}
	r.Items = items
	return r
}
func (f *copiedFixture) plan() PlanItem {
	f.t.Helper()
	r := f.report()
	var selected audit.Item
	for _, i := range r.Items {
		if i.Path == filepath.Join(f.root, "SKILL.md") {
			selected = i
		}
	}
	if selected.ID == "" {
		f.t.Fatalf("missing skill %#v", r.Items)
	}
	selection := Selection{Action: "update", IDs: []string{selected.ID}}
	targets, e := ResolveCopiedSkillTargets(context.Background(), r, selection, f.o)
	if e != nil {
		f.t.Fatal(e)
	}
	f.o.ResolvedTargets = targets
	p, e := NewPlan(r, selection, f.o)
	if e != nil {
		f.t.Fatal(e)
	}
	return p.Items[0]
}
func TestCopiedSkillPreviewAndApply(t *testing.T) {
	f := copiedSetup(t)
	lock, _ := os.ReadFile(f.lock)
	p := f.plan()
	if p.Status != "ready" || p.SkillUpdate == nil {
		t.Fatalf("preview %#v", p)
	}
	changes := p.SkillUpdate.FileChanges
	if len(changes) != 3 || changes[0].Kind != "modified" || !strings.Contains(changes[0].Diff, "-old\n+") || changes[1].Kind != "deleted" || changes[2].Kind != "added" {
		t.Fatalf("changes %#v", changes)
	}
	before, _ := os.ReadFile(filepath.Join(f.root, "SKILL.md"))
	if !strings.Contains(string(before), "old") {
		t.Fatal("preview changed installation")
	}
	backup, changed, e := applyCopiedSkill(context.Background(), p, f.o, "")
	if e != nil || !changed || backup == "" {
		t.Fatalf("apply %q %v %v", backup, changed, e)
	}
	if !verifyCopiedSkill(p, f.o) {
		t.Fatal("verification failed")
	}
	if _, e = os.Stat(filepath.Join(f.root, "deleted.txt")); !os.IsNotExist(e) {
		t.Fatal("deleted file remains")
	}
	st, _ := os.Stat(filepath.Join(f.root, "scripts/run.sh"))
	if st.Mode().Perm()&0111 == 0 {
		t.Fatal("mode not preserved")
	}
	after, _ := os.ReadFile(f.lock)
	if string(lock) != string(after) {
		t.Fatal("external lock mutated")
	}
	backupSnap, e := audit.ReadSkillUpdateSnapshot(backup)
	if e != nil || backupSnap.Tree != p.SkillUpdate.CurrentTree {
		t.Fatal("backup not preserved")
	}
	r := f.report()
	var found bool
	for _, i := range r.Items {
		if i.Path == filepath.Join(f.root, "SKILL.md") {
			src, e := audit.SkillSourceForUpdate(i)
			if e != nil || src.Baseline != f.tree || src.ReceiptDigest == "" {
				t.Fatalf("own receipt not rediscovered %#v %v", src, e)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("updated skill missing")
	}
}
func TestCopiedSkillLocalModificationBlocks(t *testing.T) {
	f := copiedSetup(t)
	f.write(f.root, "SKILL.md", "edited locally\n", 0644)
	p := f.plan()
	if p.Status != "blocked" || p.Reason != "skill_local_modifications_present" || p.SkillUpdate == nil || !strings.Contains(p.SkillUpdate.FileChanges[0].Diff, "edited locally") {
		t.Fatalf("%#v", p)
	}
}
func TestCopiedSkillDriftAfterPreviewBlocks(t *testing.T) {
	for _, which := range []string{"local", "stage", "receipt", "root"} {
		t.Run(which, func(t *testing.T) {
			f := copiedSetup(t)
			p := f.plan()
			if p.Status != "ready" {
				t.Fatal(p)
			}
			switch which {
			case "local":
				f.write(f.root, "SKILL.md", "local edits\n", 0644)
			case "stage":
				f.write(copiedStagePath(f.o, p.SkillUpdate.StageKey), "SKILL.md", "tampered\n", 0644)
			case "receipt":
				b, _ := os.ReadFile(f.lock)
				f.write(filepath.Dir(f.lock), filepath.Base(f.lock), strings.Replace(string(b), "example/skills", "other/skills", 1), 0644)
			case "root":
				if e := os.Rename(f.root, f.root+"-old"); e != nil {
					t.Fatal(e)
				}
				f.write(f.root, "SKILL.md", "replacement\n", 0644)
			}
			_, changed, e := applyCopiedSkill(context.Background(), p, f.o, "")
			if e == nil || changed {
				t.Fatalf("drift accepted %v %v", changed, e)
			}
		})
	}
}
func TestCopiedSkillAliasAndIndependentCopy(t *testing.T) {
	f := copiedSetup(t)
	alias := filepath.Join(f.home, ".codex/skills/example")
	if e := os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(f.root, alias); e != nil {
		t.Fatal(e)
	}
	copyRoot := filepath.Join(f.home, ".claude/skills/example")
	for _, name := range []string{"SKILL.md", "deleted.txt"} {
		data, _ := os.ReadFile(filepath.Join(f.root, name))
		f.write(copyRoot, name, string(data), 0644)
	}
	p := f.plan()
	if p.Status != "ready" {
		t.Fatal(p)
	}
	if _, _, e := applyCopiedSkill(context.Background(), p, f.o, ""); e != nil {
		t.Fatal(e)
	}
	if st, e := os.Lstat(alias); e != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("alias replaced")
	}
	b, _ := os.ReadFile(filepath.Join(copyRoot, "SKILL.md"))
	if !strings.Contains(string(b), "old") {
		t.Fatal("independent copy mutated")
	}
	r := f.report()
	for _, i := range r.Items {
		if i.Path == filepath.Join(copyRoot, "SKILL.md") {
			src, e := audit.SkillSourceForUpdate(i)
			if e != nil || src.Baseline == f.tree {
				t.Fatal("independent source lost or advanced")
			}
		}
	}
}
func TestCopiedSkillBadBlobBlocks(t *testing.T) {
	f := copiedSetup(t)
	f.badBlob = true
	p := f.plan()
	if p.Status != "blocked" || p.Reason != "skill_blob_hash_mismatch" {
		t.Fatalf("%#v", p)
	}
}
func TestCopiedSkillRollbackFailures(t *testing.T) {
	for _, mode := range []string{"receipt", "receipt_after_write", "rename"} {
		t.Run(mode, func(t *testing.T) {
			f := copiedSetup(t)
			p := f.plan()
			if p.Status != "ready" {
				t.Fatal(p)
			}
			rename := os.Rename
			write := writeSkillAtomic
			if mode == "rename" {
				rename = func(from, to string) error {
					if to == f.root && filepath.Base(from) == "updated" {
						return errors.New("injected rename failure")
					}
					return os.Rename(from, to)
				}
			} else {
				write = func(path string, data []byte) error {
					if mode == "receipt_after_write" {
						if e := writeSkillAtomic(path, data); e != nil {
							return e
						}
					}
					return errors.New("injected receipt failure")
				}
			}
			_, changed, e := applyCopiedSkillWithOps(context.Background(), p, f.o, rename, write)
			if e == nil || changed {
				t.Fatalf("failure not rolled back %v %v", changed, e)
			}
			snap, e := audit.ReadSkillUpdateSnapshot(f.root)
			if e != nil || snap.Tree != p.SkillUpdate.CurrentTree {
				t.Fatal("old installation not restored")
			}
			if _, e = os.Stat(audit.SkillReceiptPath(f.home, f.root)); !os.IsNotExist(e) {
				t.Fatal("failed update receipt remains")
			}
			if _, e = os.Stat(filepath.Join(copiedTransactionPath(f.o, f.root), "last-rollback.json")); e != nil {
				t.Fatal("rollback manifest missing")
			}
		})
	}
}
func TestCopiedSkillRollbackDoesNotOverwriteConcurrentEdits(t *testing.T) {
	f := copiedSetup(t)
	p := f.plan()
	write := func(string, []byte) error {
		f.write(f.root, "SKILL.md", "external concurrent edit\n", 0644)
		return errors.New("receipt failure")
	}
	backup, changed, e := applyCopiedSkillWithOps(context.Background(), p, f.o, os.Rename, write)
	if e == nil || e.Error() != "skill_recovery_required" || !changed || backup == "" {
		t.Fatalf("%s %v %v", backup, changed, e)
	}
	b, _ := os.ReadFile(filepath.Join(f.root, "SKILL.md"))
	if string(b) != "external concurrent edit\n" {
		t.Fatal("external edit overwritten")
	}
	if _, e = os.Stat(filepath.Join(backup, "SKILL.md")); e != nil {
		t.Fatal("backup unavailable")
	}
	if again := f.plan(); again.Reason != "skill_recovery_required" {
		t.Fatalf("pending recovery not blocked %#v", again)
	}
}
func TestCopiedSkillPreviewLimitAndBinary(t *testing.T) {
	old := audit.SkillUpdateSnapshot{Files: []audit.SkillUpdateFile{{Path: "binary", SHA: "old", Data: []byte{0, 1}}}}
	new := audit.SkillUpdateSnapshot{Files: []audit.SkillUpdateFile{{Path: "binary", SHA: "new", Data: []byte{0, 2}}}}
	changes, e := skillChanges(old, new)
	if e != nil || len(changes) != 1 || !changes[0].Binary || changes[0].Diff != "" || changes[0].OldSHA != "old" || changes[0].NewSHA != "new" {
		t.Fatal(changes, e)
	}
	new.Files = []audit.SkillUpdateFile{{Path: "large.txt", SHA: "new", Data: []byte(strings.Repeat("x", 1<<20))}}
	if _, e = skillChanges(audit.SkillUpdateSnapshot{}, new); e == nil {
		t.Fatal("oversize preview accepted")
	}
}

func TestCopiedSkillSecondUpdateReceiptRollback(t *testing.T) {
	f := copiedSetup(t)
	first := f.plan()
	if _, _, e := applyCopiedSkill(context.Background(), first, f.o, ""); e != nil {
		t.Fatal(e)
	}
	receiptPath := audit.SkillReceiptPath(f.home, f.root)
	receiptBefore, e := os.ReadFile(receiptPath)
	if e != nil {
		t.Fatal(e)
	}
	f.write(f.remote, "SKILL.md", "---\nname: example\n---\nthird version\n", 0644)
	f.commit = strings.Repeat("d", 40)
	f.tree = f.remoteTree(f.remote)
	second := f.plan()
	if second.Status != "ready" || second.SkillUpdate.Source.ReceiptDigest == "" {
		t.Fatalf("second update not resolved %#v", second)
	}
	write := func(p string, data []byte) error {
		if e := writeSkillAtomic(p, data); e != nil {
			return e
		}
		return errors.New("receipt durability failure")
	}
	_, changed, e := applyCopiedSkillWithOps(context.Background(), second, f.o, os.Rename, write)
	if e == nil || changed {
		t.Fatalf("%v %v", changed, e)
	}
	after, e := os.ReadFile(receiptPath)
	if e != nil || string(after) != string(receiptBefore) {
		t.Fatal("original application receipt not restored")
	}
	if !verifyCopiedSkill(first, f.o) {
		t.Fatal("previous successful version not restored")
	}
}
func TestCopiedSkillAliasSelectionReplacesOnce(t *testing.T) {
	f := copiedSetup(t)
	alias := filepath.Join(f.home, ".codex/skills/example")
	if e := os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(f.root, alias); e != nil {
		t.Fatal(e)
	}
	r := f.report()
	sel := Selection{Action: "update", All: true}
	targets, e := ResolveCopiedSkillTargets(context.Background(), r, sel, f.o)
	if e != nil {
		t.Fatal(e)
	}
	f.o.ResolvedTargets = targets
	p, e := NewPlan(r, sel, f.o)
	if e != nil {
		t.Fatal(e)
	}
	ready, skipped := 0, 0
	for _, item := range p.Items {
		if item.Status == "ready" {
			ready++
		}
		if item.Reason == "duplicate_installation_target" {
			skipped++
		}
	}
	if ready != 1 || skipped != 1 {
		t.Fatalf("ready=%d skipped=%d items=%#v", ready, skipped, p.Items)
	}
}

func TestCopiedSkillOfflineSnapshotEngineApply(t *testing.T) {
	f := copiedSetup(t)
	snapshot := func(context.Context) (audit.Report, error) { return f.report(), nil }
	r, e := snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if r.Online {
		t.Fatal("fixture must use offline scanner")
	}
	var id string
	for _, i := range r.Items {
		if i.Path == filepath.Join(f.root, "SKILL.md") {
			id = i.ID
		}
		for _, e := range i.Evidence {
			if strings.HasPrefix(e, "skill-upstream-") {
				t.Fatal("offline snapshot unexpectedly contains upstream evidence")
			}
		}
	}
	selection := Selection{Action: "update", IDs: []string{id}}
	resolve := func(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
		return ResolveCopiedSkillTargets(ctx, r, s, f.o)
	}
	targets, e := resolve(context.Background(), r, selection)
	if e != nil {
		t.Fatal(e)
	}
	f.o.ResolvedTargets = targets
	p, e := NewPlan(r, selection, f.o)
	if e != nil || len(p.Items) != 1 || p.Items[0].Status != "ready" {
		t.Fatalf("offline plan %#v %v", p, e)
	}
	engine := Engine{Options: f.o, Snapshot: snapshot, ResolveTargets: resolve, StateDir: filepath.Join(f.home, "actions"), Now: func() time.Time { return f.o.Now.Add(time.Minute) }, Runner: runFunc(func(context.Context, Command) ([]byte, error) {
		t.Fatal("copied skill must not execute commands")
		return nil, nil
	})}
	out, e := Apply(context.Background(), p, p.PlanID, engine)
	if e != nil || len(out.Results) != 1 || out.Results[0].Status != "updated" || !out.Results[0].Changed || out.Results[0].BackupPath == "" {
		t.Fatalf("offline apply %#v %v", out, e)
	}
	if !verifyCopiedSkill(p.Items[0], f.o) {
		t.Fatal("offline source readback verification failed")
	}
	for _, item := range f.report().Items {
		if item.ID == id && item.Version != f.tree[:12] {
			t.Fatal("offline readback lost current content version", item.Version)
		}
	}
	counts := map[string]int{}
	for _, request := range f.requests {
		for _, kind := range []string{"commits", "git/trees", "git/blobs"} {
			if strings.Contains(request, "/"+kind+"/") {
				counts[kind]++
			}
		}
	}
	if counts["commits"] != 3 || counts["git/trees"] != len(f.trees) || counts["git/blobs"] != len(f.blobs) {
		t.Fatalf("expected fresh refs and one fetch per immutable object; requests=%v counts=%v", f.requests, counts)
	}

}
func TestCopiedSkillOfflineSnapshotRejectsMovedRef(t *testing.T) {
	f := copiedSetup(t)
	r := f.report()
	id := r.Items[0].ID
	selection := Selection{Action: "update", IDs: []string{id}}
	resolve := func(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
		return ResolveCopiedSkillTargets(ctx, r, s, f.o)
	}
	targets, e := resolve(context.Background(), r, selection)
	if e != nil {
		t.Fatal(e)
	}
	f.o.ResolvedTargets = targets
	p, e := NewPlan(r, selection, f.o)
	if e != nil || p.Items[0].Status != "ready" {
		t.Fatal(p, e)
	}
	f.commit = strings.Repeat("d", 40)
	engine := Engine{Options: f.o, Snapshot: func(context.Context) (audit.Report, error) { return f.report(), nil }, ResolveTargets: resolve, StateDir: filepath.Join(f.home, "actions"), Now: func() time.Time { return f.o.Now.Add(time.Minute) }}
	if _, e = Apply(context.Background(), p, p.PlanID, engine); e == nil {
		t.Fatal("moved upstream ref did not invalidate approved preview")
	}
	snap, e := audit.ReadSkillUpdateSnapshot(f.root)
	if e != nil || snap.Tree != p.Items[0].SkillUpdate.CurrentTree {
		t.Fatal("ref change modified installation")
	}
}
func TestCopiedSkillRollbackRestoresEditedOriginalInode(t *testing.T) {
	f := copiedSetup(t)
	p := f.plan()
	rename := func(from, to string) error {
		if e := os.Rename(from, to); e != nil {
			return e
		}
		if from == f.root && filepath.Base(to) == "original" {
			f.write(to, "SKILL.md", "concurrent original edit\n", 0644)
		}
		return nil
	}
	_, changed, e := applyCopiedSkillWithOps(context.Background(), p, f.o, rename, writeSkillAtomic)
	if e == nil || changed {
		t.Fatalf("%v %v", changed, e)
	}
	data, e := os.ReadFile(filepath.Join(f.root, "SKILL.md"))
	if e != nil || string(data) != "concurrent original edit\n" {
		t.Fatalf("concurrent original not restored %q %v", data, e)
	}
}
func TestCopiedSkillRollbackKeepsInstalledRootWhenBackupUnavailable(t *testing.T) {
	for _, mode := range []string{"missing", "replaced", "restore_rename_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := copiedSetup(t)
			p := f.plan()
			var backup string
			rename := func(from, to string) error {
				if from == f.root && filepath.Base(to) == "original" {
					backup = to
				}
				if mode == "restore_rename_failure" && from == backup && to == f.root {
					return errors.New("restore failed")
				}
				return os.Rename(from, to)
			}
			write := func(string, []byte) error {
				if mode != "restore_rename_failure" {
					if e := os.Rename(backup, backup+"-retained"); e != nil {
						t.Fatal(e)
					}
					if mode == "replaced" {
						f.write(backup, "SKILL.md", "external replacement\n", 0644)
					}
				}
				return errors.New("receipt failed")
			}
			_, changed, e := applyCopiedSkillWithOps(context.Background(), p, f.o, rename, write)
			if e == nil || e.Error() != "skill_recovery_required" || !changed {
				t.Fatalf("%v %v", changed, e)
			}
			snap, e := audit.ReadSkillUpdateSnapshot(f.root)
			if e != nil || snap.Tree != p.SkillUpdate.Tree {
				t.Fatalf("usable installed root disappeared %v %v", snap, e)
			}
			if mode == "replaced" {
				data, e := os.ReadFile(filepath.Join(backup, "SKILL.md"))
				if e != nil || string(data) != "external replacement\n" {
					t.Fatal("external backup replacement changed")
				}
			}
		})
	}
}

func TestCopiedSkillCachedObjectsAlwaysReverified(t *testing.T) {
	for _, kind := range []string{"blob", "tree"} {
		t.Run(kind, func(t *testing.T) {
			f := copiedSetup(t)
			p := f.plan()
			if p.Status != "ready" {
				t.Fatal(p)
			}
			sha := f.tree
			if kind == "blob" {
				for id := range f.blobs {
					sha = id
					break
				}
			}
			cacheFile := filepath.Join(copiedSkillState(f.o), "skill-objects", kind+"-"+sha)
			if e := os.WriteFile(cacheFile, []byte("corrupted object"), 0600); e != nil {
				t.Fatal(e)
			}
			again := f.plan()
			if again.Status != "blocked" {
				t.Fatal("corrupted cached object accepted", again)
			}
		})
	}
}

func TestCopiedSkillDiffShowsActualHunksAndExactLineEndings(t *testing.T) {
	before := strings.Repeat("unchanged\n", 20) + "old\n" + strings.Repeat("tail\n", 20)
	after := strings.Repeat("unchanged\n", 20) + "new\n" + strings.Repeat("tail\n", 20)
	diff, err := fullSkillDiff("SKILL.md", []byte(before), []byte(after))
	if err != nil || !strings.Contains(diff, "-old\n+new\n") || strings.Contains(diff, "-unchanged") || len(diff) > 250 {
		t.Fatal(diff, err)
	}
	diff, err = fullSkillDiff("text", []byte("old\r\nlast"), []byte("new\nlast\n"))
	if err != nil || !strings.Contains(diff, "-old\r\n") || !strings.Contains(diff, "No newline at end of file") {
		t.Fatal(diff, err)
	}
}

func TestCopiedSkillExplicitProjectReceiptUpdate(t *testing.T) {
	f := copiedSetup(t)
	project := filepath.Join(f.home, "project")
	root := filepath.Join(project, ".agents/skills/example")
	if err := os.MkdirAll(filepath.Dir(root), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.root, root); err != nil {
		t.Fatal(err)
	}
	f.root = root
	f.projects = []string{project}
	f.o.SkillRoots = append(f.o.SkillRoots, filepath.Dir(root))
	snap, err := audit.ReadSkillUpdateSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, file := range snap.Files {
		h.Write([]byte(file.Path))
		h.Write(file.Data)
	}
	f.lock = filepath.Join(project, "skills-lock.json")
	b, _ := json.Marshal(map[string]any{"version": 1, "skills": map[string]any{"example": map[string]any{"source": "example/skills", "sourceType": "github", "sourceUrl": "https://github.com/example/skills", "skillPath": "SKILL.md", "computedHash": hex.EncodeToString(h.Sum(nil))}}})
	f.write(project, "skills-lock.json", string(b), 0644)
	p := f.plan()
	if p.Status != "ready" {
		t.Fatal(p.Reason)
	}
	if _, changed, err := applyCopiedSkill(context.Background(), p, f.o, ""); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if !verifyCopiedSkill(p, f.o) {
		t.Fatal("project update did not verify")
	}
	if data, err := os.ReadFile(f.lock); err != nil || string(data) != string(b) {
		t.Fatal("external project receipt changed", err)
	}
}

func TestCopiedSkillBatchUpdatesIndependentCopiesAndKeepsSharedLinks(t *testing.T) {
	f := copiedSetup(t)
	copyRoot := filepath.Join(f.home, ".codex/skills/example")
	for _, name := range []string{"SKILL.md", "deleted.txt"} {
		b, err := os.ReadFile(filepath.Join(f.root, name))
		if err != nil {
			t.Fatal(err)
		}
		f.write(copyRoot, name, string(b), 0644)
	}
	alias := filepath.Join(f.home, ".claude/skills/example")
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	r := f.report()
	selection := Selection{Action: "update", All: true}
	resolve := func(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
		return ResolveCopiedSkillTargets(ctx, r, s, f.o)
	}
	targets, err := resolve(context.Background(), r, selection)
	if err != nil {
		t.Fatal(err)
	}
	f.o.ResolvedTargets = targets
	p, err := NewPlan(r, selection, f.o)
	if err != nil {
		t.Fatal(err)
	}
	ready, duplicates := 0, 0
	for _, item := range p.Items {
		if item.Status == "ready" {
			ready++
		}
		if item.Reason == "duplicate_installation_target" {
			duplicates++
			if item.SkillUpdate != nil {
				t.Fatal("duplicate preview retained full diff")
			}
		}
	}
	if ready != 2 || duplicates != 1 {
		t.Fatalf("ready=%d duplicate=%d", ready, duplicates)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePlan(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	engine := Engine{Options: f.o, Snapshot: func(context.Context) (audit.Report, error) { return f.report(), nil }, ResolveTargets: resolve, StateDir: filepath.Join(f.home, "actions"), Now: func() time.Time { return f.o.Now.Add(time.Minute) }}
	out, err := Apply(context.Background(), decoded, decoded.PlanID, engine)
	if err != nil {
		t.Fatal(err)
	}
	updated := 0
	for _, item := range out.Results {
		if item.Status == "updated" {
			updated++
		}
	}
	if updated != 2 {
		t.Fatal(out.Results)
	}
	for _, root := range []string{f.root, copyRoot} {
		snap, err := audit.ReadSkillUpdateSnapshot(root)
		if err != nil || snap.Tree != f.tree {
			t.Fatal("copy not updated", root, err)
		}
	}
	if target, err := os.Readlink(alias); err != nil || target != f.root {
		t.Fatal("shared link was replaced", err)
	}
}

func TestCopiedSkillUpdatePreservesLocalReadWritePermissions(t *testing.T) {
	f := copiedSetup(t)
	if err := os.Chmod(filepath.Join(f.root, "SKILL.md"), 0600); err != nil {
		t.Fatal(err)
	}
	p := f.plan()
	if p.Status != "ready" {
		t.Fatal(p.Reason)
	}
	if _, _, err := applyCopiedSkill(context.Background(), p, f.o, ""); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(f.root, "SKILL.md"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("update broadened file access", st, err)
	}
	if !verifyCopiedSkill(p, f.o) {
		t.Fatal("content verification failed after preserving permissions")
	}
}

func TestCopiedSkillPinnedReceiptBlocksPreviewAndApply(t *testing.T) {
	for _, pin := range []any{"v1", "v2", true, nil} {
		t.Run(fmt.Sprint(pin), func(t *testing.T) {
			f := copiedSetup(t)
			p := f.plan()
			if p.Status != "ready" {
				t.Fatal(p)
			}
			before, err := audit.ReadSkillUpdateSnapshot(f.root)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(f.lock)
			if err != nil {
				t.Fatal(err)
			}
			var lock map[string]any
			if err = json.Unmarshal(b, &lock); err != nil {
				t.Fatal(err)
			}
			entry := lock["skills"].(map[string]any)["example"].(map[string]any)
			entry["pinnedRef"] = pin
			b, err = json.Marshal(lock)
			if err != nil {
				t.Fatal(err)
			}
			f.write(filepath.Dir(f.lock), filepath.Base(f.lock), string(b), 0644)
			requests := len(f.requests)
			if _, changed, err := applyCopiedSkill(context.Background(), p, f.o, ""); err == nil || changed {
				t.Fatal("pin drift applied", changed, err)
			}
			if got := f.plan(); got.Status == "ready" || got.Reason != "skill_source_unverified" {
				t.Fatal(got)
			}
			if len(f.requests) != requests {
				t.Fatal("pinned update downloaded")
			}
			after, err := audit.ReadSkillUpdateSnapshot(f.root)
			if err != nil || before.Tree != after.Tree {
				t.Fatal("installation changed", err)
			}
			got, err := os.ReadFile(f.lock)
			if err != nil || string(got) != string(b) {
				t.Fatal("external lock changed", err)
			}
		})
	}
}

func TestCopiedSkillInjectedPayloadStaysBlocked(t *testing.T) {
	f := copiedSetup(t)
	f.write(f.root, "SKILL.md", "---\nname: example\ngithub-pinned: v1\ngithub-source: example/skills\n---\nold\n", 0644)
	p := f.plan()
	if p.Status != "blocked" || p.Reason != "skill_local_modifications_present" {
		t.Fatal(p)
	}
}

func TestCopiedSkillPinProtectsDifferentlyNamedAliases(t *testing.T) {
	for _, alias := range []string{"aaa", "zzz"} {
		t.Run(alias, func(t *testing.T) {
			f := copiedSetup(t)
			aliasRoot := filepath.Join(f.home, ".agents/skills", alias)
			if err := os.Symlink(f.root, aliasRoot); err != nil {
				t.Fatal(err)
			}
			independent := filepath.Join(f.home, ".claude/skills/example")
			for _, name := range []string{"SKILL.md", "deleted.txt"} {
				data, err := os.ReadFile(filepath.Join(f.root, name))
				if err != nil {
					t.Fatal(err)
				}
				f.write(independent, name, string(data), 0644)
			}
			before, err := audit.ReadSkillUpdateSnapshot(f.root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(f.lock)
			if err != nil {
				t.Fatal(err)
			}
			var lock map[string]any
			if err = json.Unmarshal(data, &lock); err != nil {
				t.Fatal(err)
			}
			entries := lock["skills"].(map[string]any)
			pinned := map[string]any{}
			for k, v := range entries["example"].(map[string]any) {
				pinned[k] = v
			}
			pinned["pinnedRef"] = "v1"
			entries[alias] = pinned
			data, err = json.Marshal(lock)
			if err != nil {
				t.Fatal(err)
			}
			f.write(filepath.Dir(f.lock), filepath.Base(f.lock), string(data), 0644)
			r := f.report()
			if len(r.Items) != 3 {
				t.Fatal(r.Items)
			}
			selection := Selection{Action: "update", All: true}
			f.o.ResolvedTargets, err = ResolveCopiedSkillTargets(context.Background(), r, selection, f.o)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := NewPlan(r, selection, f.o)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Items) != 3 {
				t.Fatal("missing alias or independent copy", plan.Items)
			}
			for _, p := range plan.Items {
				var item audit.Item
				for _, i := range r.Items {
					if i.ID == p.ID {
						item = i
					}
				}
				if item.Path == filepath.Join(independent, "SKILL.md") {
					if p.Status != "ready" {
						t.Fatal("independent copy lost its source", p)
					}
					continue
				}
				if _, changed, err := applyCopiedSkill(context.Background(), p, f.o, ""); err == nil || changed {
					t.Fatal("pinned shared directory changed", item.Path, changed, err)
				}
				if p.Status != "blocked" || p.Reason != "skill_source_unverified" {
					t.Fatal(p)
				}
			}
			after, err := audit.ReadSkillUpdateSnapshot(aliasRoot)
			if err != nil || before.Tree != after.Tree {
				t.Fatal("pinned alias content changed", err)
			}
			got, err := os.ReadFile(f.lock)
			if err != nil || string(got) != string(data) {
				t.Fatal("external lock changed", err)
			}
		})
	}
}
