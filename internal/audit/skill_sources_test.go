package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillLock(t *testing.T, p string, version int, entries map[string]skillReceipt) {
	t.Helper()
	b, err := json.Marshal(skillLock{Version: version, Skills: entries})
	if err != nil {
		t.Fatal(err)
	}
	write(t, p, string(b))
}
func sourceReceipt(tree string) skillReceipt {
	return skillReceipt{Source: "example/skills", SourceType: "github", SourceURL: "https://github.com/example/skills.git", SkillPath: "skills/example/SKILL.md", FolderHash: tree}
}
func sourceSkill(t *testing.T, s *Scanner, name string) (string, skillSnapshot) {
	t.Helper()
	p := filepath.Join(s.Config.Home, ".agents/skills", name, "SKILL.md")
	write(t, p, "---\nname: "+name+"\n---\n")
	snap, err := readSkillSnapshot(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	return p, snap
}
func serveSkillTrees(t *testing.T, s *Scanner, trees map[string]skillTree, root string) *[]string {
	t.Helper()
	var calls []string
	s.feedClient.Transport = transport(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.Header.Get("Authorization") != "" || req.URL.Host != "api.github.com" || req.URL.RawQuery != "" {
			t.Fatalf("unexpected request: %v", req)
		}
		calls = append(calls, req.URL.EscapedPath())
		if strings.Contains(req.URL.Path, "/commits/") {
			b, _ := json.Marshal(map[string]any{"sha": strings.Repeat("c", 40), "commit": map[string]any{"tree": map[string]string{"sha": root}}})
			return response(200, string(b)), nil
		}
		tree, ok := trees[filepath.Base(req.URL.Path)]
		if !ok {
			return response(404, ""), nil
		}
		b, _ := json.Marshal(tree)
		return response(200, string(b)), nil
	})
	return &calls
}
func treeForSnapshot(snapshot skillSnapshot) skillTree {
	tree := skillTree{SHA: snapshot.tree}
	for _, f := range snapshot.files {
		tree.Tree = append(tree.Tree, skillTreeEntry{Path: f.name, Type: "blob", Mode: f.mode, SHA: f.hash})
	}
	return tree
}
func attachSource(t *testing.T, s *Scanner, p string, r skillReceipt, kind string) {
	t.Helper()
	source, ok := parsedSkillSource(r, kind)
	if !ok {
		t.Fatal(r)
	}
	source.root = filepath.Dir(real(p))
	i := item("skill", "example", p, ".agents")
	i.Source = "local"
	i.skillSource = source
	s.Report.Items = append(s.Report.Items, i)
}
func TestSkillObjectMatchesGitVector(t *testing.T) {
	if got := skillObject("blob", []byte("test content\n")); got != "d670460b4b4aece5915caf5c68d12f560a9fe3e4" {
		t.Fatal(got)
	}
	if got := skillObject("tree", nil); got != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Fatal(got)
	}
}
func TestSkillSourceValidation(t *testing.T) {
	good := sourceReceipt(strings.Repeat("a", 40))
	good.Ref = "feature/fix-1"
	if _, ok := parsedSkillSource(good, "tree"); !ok {
		t.Fatal("valid source rejected")
	}
	cases := []skillReceipt{}
	for _, url := range []string{"https://user:SECRET@github.com/example/skills", "https://github.com/other/skills", "http://github.com/example/skills", "https://github.com/example/skills?token=SECRET", "https://github.com/example/skills#main", "https://github.com/example/skills/tree/main"} {
		r := good
		r.SourceURL = url
		cases = append(cases, r)
	}
	for _, p := range []string{"../SKILL.md", "/SKILL.md", "skills/../SKILL.md", "skills\\SKILL.md", "skills/example/README.md"} {
		r := good
		r.SkillPath = p
		cases = append(cases, r)
	}
	for _, ref := range []string{"a..b", "../main", "main?secret=1", "main\n", "main//test"} {
		r := good
		r.Ref = ref
		cases = append(cases, r)
	}
	for _, owner := range []string{"gh", "unknown", "local"} {
		r := good
		r.SourceType = owner
		cases = append(cases, r)
	}
	for _, r := range cases {
		if _, ok := parsedSkillSource(r, "tree"); ok {
			t.Fatal("unsafe source accepted", r)
		}
	}
	noHash := good
	noHash.FolderHash = ""
	if _, ok := parsedSkillSource(noHash, "tree"); !ok {
		t.Fatal("source identity without baseline should stay detectable")
	}
}
func TestSkillReceiptsBindCanonicalAliasesNotNameAlone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	s := fixture(t)
	p, snap := sourceSkill(t, s, "example")
	alias := filepath.Join(s.Config.Home, ".codex/skills/example")
	link(t, filepath.Dir(p), alias)
	copyPath := filepath.Join(s.Config.Home, ".claude/skills/example/SKILL.md")
	write(t, copyPath, "---\nmodified locally\n---\n")
	writeSkillLock(t, filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), 3, map[string]skillReceipt{"example": sourceReceipt(snap.tree)})
	s.skills()
	s.skillSources()
	bound := 0
	for _, i := range s.Report.Items {
		if i.skillSource != nil {
			bound++
			if i.RealPath != real(p) {
				t.Fatal(i)
			}
		}
		if i.Path == copyPath && i.skillSource != nil {
			t.Fatal("unrelated edited same-name copy bound")
		}
	}
	if bound != 2 {
		t.Fatal(bound, s.Report.Items)
	}
}
func TestSkillReceiptConflictsDoNotChooseARepository(t *testing.T) {
	s := fixture(t)
	_, snap := sourceSkill(t, s, "example")
	xdg := filepath.Join(s.Config.Home, "state")
	t.Setenv("XDG_STATE_HOME", xdg)
	r := sourceReceipt(snap.tree)
	writeSkillLock(t, filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), 3, map[string]skillReceipt{"example": r})
	r.Source = "other/skills"
	r.SourceURL = "https://github.com/other/skills"
	writeSkillLock(t, filepath.Join(xdg, "skills/.skill-lock.json"), 3, map[string]skillReceipt{"example": r})
	s.skills()
	s.skillSources()
	if len(s.Report.Items) != 1 || s.Report.Items[0].skillSource != nil || s.Report.Items[0].Update.Code != "skill_source_conflict" {
		t.Fatal(s.Report.Items)
	}
}
func TestSkillSourcesPreserveSystemPluginAndGitOwnership(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	s := fixture(t)
	p, snap := sourceSkill(t, s, "example")
	writeSkillLock(t, filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), 3, map[string]skillReceipt{"example": sourceReceipt(snap.tree)})
	for _, scope := range []struct{ owner, source string }{{"system", "local"}, {"plugin-cache", "plugin-cache"}, {".agents", "git"}} {
		i := item("skill", "example", p, scope.owner)
		i.Source = scope.source
		s.Report.Items = append(s.Report.Items, i)
	}
	s.skillSources()
	for _, i := range s.Report.Items {
		if i.skillSource != nil {
			t.Fatal(i)
		}
	}
}
func TestSkillGitTreeDetectsHiddenFilesAndExecutableMode(t *testing.T) {
	s := fixture(t)
	p, baseline := sourceSkill(t, s, "example")
	write(t, filepath.Join(filepath.Dir(p), ".local-edits"), "private")
	modified, err := readSkillSnapshot(filepath.Dir(p))
	if err != nil || modified.tree == baseline.tree {
		t.Fatal(err, modified)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(p), ".local-edits")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0755); err != nil {
		t.Fatal(err)
	}
	modified, err = readSkillSnapshot(filepath.Dir(p))
	if err != nil || modified.tree == baseline.tree {
		t.Fatal(err, modified)
	}
}
func TestSkillGlobalStatesAndReadOnlyQueries(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		upstream, local bool
		status, code    string
	}{{"current", false, false, "current", "skill_content_current"}, {"upstream", true, false, "available", "skill_update_available"}, {"local", false, true, "unknown", "skill_local_modified"}, {"both", true, true, "unknown", "skill_upstream_changed_local_modified"}} {
		t.Run(scenario.name, func(t *testing.T) {
			s := fixture(t)
			s.Config.Online = true
			p, base := sourceSkill(t, s, "example")
			r := sourceReceipt(base.tree)
			r.SkillPath = "SKILL.md"
			attachSource(t, s, p, r, "tree")
			remote := base
			if scenario.upstream {
				temp := t.TempDir()
				write(t, filepath.Join(temp, "SKILL.md"), "---\nupstream update\n---\n")
				remote, _ = readSkillSnapshot(temp)
			}
			if scenario.local {
				write(t, p, "---\nlocal edits\n---\n")
			}
			before, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			calls := serveSkillTrees(t, s, map[string]skillTree{remote.tree: treeForSnapshot(remote)}, remote.tree)
			s.checkSkillSources()
			i := s.Report.Items[0]
			if i.Update.Status != scenario.status || i.Update.Code != scenario.code {
				t.Fatal(i)
			}
			if len(*calls) != 2 {
				t.Fatal(*calls)
			}
			after, _ := os.ReadFile(p)
			if string(before) != string(after) {
				t.Fatal("check mutated skill")
			}
		})
	}
}
func TestSkillQueriesBatchSameRepositoryRefAndPinSlashRef(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	p, snap := sourceSkill(t, s, "example")
	r := sourceReceipt(snap.tree)
	r.Ref = "feature/slash"
	r.SkillPath = "example/SKILL.md"
	attachSource(t, s, p, r, "tree")
	q := filepath.Join(s.Config.Home, "alias/SKILL.md")
	link(t, p, q)
	attachSource(t, s, q, r, "tree")
	root := strings.Repeat("a", 40)
	trees := map[string]skillTree{root: {SHA: root, Tree: []skillTreeEntry{{Path: "example", Type: "tree", Mode: "040000", SHA: snap.tree}}}, snap.tree: treeForSnapshot(snap)}
	calls := serveSkillTrees(t, s, trees, root)
	s.checkSkillSources()
	if len(*calls) != 3 || !strings.Contains((*calls)[0], "feature%2Fslash") {
		t.Fatal(*calls)
	}
	for _, i := range s.Report.Items {
		if i.Update.Status != "current" {
			t.Fatal(i)
		}
	}
}
func TestProjectSkillContentHashAndExplicitScope(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	s := fixture(t)
	s.Config.Online = true
	project := filepath.Join(s.Config.Home, "project")
	s.Config.Projects = []string{project}
	p := filepath.Join(project, ".agents/skills/example/SKILL.md")
	content := "---\nname: example\n---\n"
	write(t, p, content)
	h := sha256.Sum256([]byte("SKILL.md" + content))
	r := sourceReceipt("")
	r.ComputedHash = hex.EncodeToString(h[:])
	r.SkillPath = "SKILL.md"
	writeSkillLock(t, filepath.Join(project, "skills-lock.json"), 1, map[string]skillReceipt{"example": r})
	s.skills()
	s.skillSources()
	if len(s.Report.Items) != 1 || s.Report.Items[0].skillSource == nil {
		t.Fatal(s.Report.Items)
	}
	remote := t.TempDir()
	write(t, filepath.Join(remote, "SKILL.md"), "---\nupstream changed\n---\n")
	latest, _ := readSkillSnapshot(remote)
	serveSkillTrees(t, s, map[string]skillTree{latest.tree: treeForSnapshot(latest)}, latest.tree)
	s.checkSkillSources()
	if s.Report.Items[0].Update.Code != "skill_update_available" {
		t.Fatal(s.Report.Items)
	}
}
func TestSkillUnknownBaselineNeverAdvertisesAnAutomaticUpdate(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	p, _ := sourceSkill(t, s, "example")
	r := sourceReceipt("")
	r.SkillPath = "SKILL.md"
	attachSource(t, s, p, r, "tree")
	remote := t.TempDir()
	write(t, filepath.Join(remote, "SKILL.md"), "---\nnew\n---\n")
	latest, _ := readSkillSnapshot(remote)
	serveSkillTrees(t, s, map[string]skillTree{latest.tree: treeForSnapshot(latest)}, latest.tree)
	s.checkSkillSources()
	if s.Report.Items[0].Update.Status != "unknown" || s.Report.Items[0].Update.Code != "skill_local_modified_or_baseline_unknown" {
		t.Fatal(s.Report.Items)
	}
}
func TestSkillTreeFailuresStayUnknownOrError(t *testing.T) {
	for _, scenario := range []string{"missing", "truncated", "wrong-tree", "network"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixture(t)
			s.Config.Online = true
			p, snap := sourceSkill(t, s, "example")
			r := sourceReceipt(snap.tree)
			r.SkillPath = "SKILL.md"
			attachSource(t, s, p, r, "tree")
			tree := treeForSnapshot(snap)
			switch scenario {
			case "missing":
				tree.Tree = nil
			case "truncated":
				tree.Truncated = true
			case "wrong-tree":
				tree.SHA = strings.Repeat("b", 40)
			}
			serveSkillTrees(t, s, map[string]skillTree{snap.tree: tree}, snap.tree)
			if scenario == "network" {
				s.feedClient.Transport = transport(func(*http.Request) (*http.Response, error) { return response(403, ""), nil })
			}
			s.checkSkillSources()
			if state := s.Report.Items[0].Update.Status; state == "available" || state == "current" {
				t.Fatal(s.Report.Items)
			}
		})
	}
}
func TestOfflineSkillReceiptsAndLimitsNeverWriteOrQuery(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	s := fixture(t)
	p, snap := sourceSkill(t, s, "example")
	lock := filepath.Join(s.Config.Home, ".agents/.skill-lock.json")
	writeSkillLock(t, lock, 3, map[string]skillReceipt{"example": sourceReceipt(snap.tree)})
	before, _ := os.ReadFile(lock)
	s.feedClient.Transport = transport(func(*http.Request) (*http.Response, error) { t.Fatal("offline HTTP"); return nil, nil })
	s.skills()
	s.skillSources()
	s.checkSkillSources()
	after, _ := os.ReadFile(lock)
	if string(before) != string(after) || s.Report.Items[0].Update.Code != "online_query_required" {
		t.Fatal(s.Report.Items)
	}
	write(t, filepath.Join(filepath.Dir(p), "huge"), strings.Repeat("x", metadataLimit+1))
	if _, err := readSkillSnapshot(filepath.Dir(p)); err == nil {
		t.Fatal("hash byte limit bypassed")
	}
}

func TestGlobalSkillContentFallbackAndPinnedCommitMismatch(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	p, snap := sourceSkill(t, s, "example")
	r := sourceReceipt(strings.Repeat("a", 64))
	r.SkillPath = "SKILL.md"
	source, ok := parsedSkillSource(r, "tree")
	if !ok || source.hashKind != "content" {
		t.Fatal(source, ok)
	}
	r.FolderHash = snap.tree
	r.Ref = strings.Repeat("d", 40)
	attachSource(t, s, p, r, "tree")
	serveSkillTrees(t, s, map[string]skillTree{snap.tree: treeForSnapshot(snap)}, snap.tree)
	s.checkSkillSources()
	if s.Report.Items[0].Update.Code != "invalid_response" {
		t.Fatal(s.Report.Items)
	}
}

func TestModifiedSkillVersionUsesLiveContentHash(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	p, baseline := sourceSkill(t, s, "example")
	r := sourceReceipt(baseline.tree)
	r.SkillPath = "SKILL.md"
	attachSource(t, s, p, r, "tree")
	write(t, p, "---\nlocal edited content\n---\n")
	live, err := readSkillSnapshot(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	serveSkillTrees(t, s, map[string]skillTree{baseline.tree: treeForSnapshot(baseline)}, baseline.tree)
	s.checkSkillSources()
	i := s.Report.Items[0]
	if i.Version != live.tree[:12] || i.Version == baseline.tree[:12] || i.Latest != baseline.tree[:12] || i.Update.Code != "skill_local_modified" {
		t.Fatal(i)
	}
	found := false
	for _, e := range i.Evidence {
		if e == "skill-installed-baseline:"+baseline.tree {
			found = true
		}
	}
	if !found {
		t.Fatal("installed baseline evidence missing", i.Evidence)
	}
}

func TestSkillReceiptPinsStayManual(t *testing.T) {
	for _, pin := range []string{`"v1.0"`, `"feature/fix"`, `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `" "`, `true`, `42`, `{}`, `[]`, `null`} {
		t.Run(pin, func(t *testing.T) {
			s := fixture(t)
			s.Config.Online = true
			p, snap := sourceSkill(t, s, "example")
			link(t, filepath.Dir(p), filepath.Join(s.Config.Home, ".codex/skills/example"))
			copyPath := filepath.Join(s.Config.Home, ".claude/skills/example/SKILL.md")
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			write(t, copyPath, string(data))
			r := sourceReceipt(snap.tree)
			r.Ref = "main"
			r.PinnedRef = json.RawMessage(pin)
			if _, ok := parsedSkillSource(r, "tree"); ok {
				t.Fatal("pin accepted as executable source")
			}
			lock := filepath.Join(s.Config.Home, ".agents/.skill-lock.json")
			writeSkillLock(t, lock, 3, map[string]skillReceipt{"example": r})
			before, err := os.ReadFile(lock)
			if err != nil {
				t.Fatal(err)
			}
			// A second unpinned receipt must not undo the manual result.
			xdg := filepath.Join(s.Config.Home, "state")
			t.Setenv("XDG_STATE_HOME", xdg)
			r.PinnedRef = nil
			writeSkillLock(t, filepath.Join(xdg, "skills/.skill-lock.json"), 3, map[string]skillReceipt{"example": r})
			s.skills()
			s.skillSources()
			s.feedClient.Transport = transport(func(*http.Request) (*http.Response, error) {
				t.Fatal("pinned source queried upstream")
				return nil, nil
			})
			s.checkSkillSources()
			if len(s.Report.Items) != 3 {
				t.Fatal(s.Report.Items)
			}
			for _, i := range s.Report.Items {
				if i.skillSource != nil || i.Update.Code != "skill_source_unverified" || !strings.Contains(strings.Join(i.Evidence, " "), "skill-pin:unsupported") {
					t.Fatal(i)
				}
				if _, err := SkillSourceForUpdate(i); err == nil {
					t.Fatal("pin became update source")
				}
			}
			after, err := os.ReadFile(lock)
			if err != nil || string(before) != string(after) {
				t.Fatal("external receipt changed", err)
			}
		})
	}
}

func TestSkillReceiptDigestCompatibility(t *testing.T) {
	// Frozen pre-pin serialization: omitted pinnedRef must keep old plans valid.
	legacy := `{"Record":{"source":"example/skills","sourceType":"github","sourceUrl":"https://github.com/example/skills.git","ref":"","skillPath":"skills/example/SKILL.md","skillFolderHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","computedHash":""},"Kind":"tree"}`
	r := sourceReceipt(strings.Repeat("a", 40))
	h := sha256.Sum256([]byte(legacy))
	if sourceRecordDigest(r, "tree") != hex.EncodeToString(h[:]) {
		t.Fatal("ordinary v3 receipt digest changed")
	}
	old := sourceRecordDigest(r, "tree")
	for _, pin := range []string{`"v1"`, `"v2"`, `false`, `null`} {
		r.PinnedRef = json.RawMessage(pin)
		next := sourceRecordDigest(r, "tree")
		if next == old {
			t.Fatal("pin missing from digest")
		}
		old = next
	}
	r.PinnedRef = json.RawMessage(`""`)
	if _, ok := parsedSkillSource(r, "tree"); !ok {
		t.Fatal("empty string pin rejected")
	}
	r.PinnedRef = nil
	r.FolderHash = ""
	r.ComputedHash = strings.Repeat("b", 64)
	legacy = `{"Record":{"source":"example/skills","sourceType":"github","sourceUrl":"https://github.com/example/skills.git","ref":"","skillPath":"skills/example/SKILL.md","skillFolderHash":"","computedHash":"` + r.ComputedHash + `"},"Kind":"content"}`
	h = sha256.Sum256([]byte(legacy))
	if sourceRecordDigest(r, "content") != hex.EncodeToString(h[:]) {
		t.Fatal("ordinary v1 receipt digest changed")
	}
}

func TestPinnedReceiptCannotBeHiddenByInvalidIdentity(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"ref", `"../invalid"`}, {"sourceType", `"unknown"`},
		{"ref", `true`}, {"sourceType", `true`},
	} {
		t.Run(tc.field+"/"+tc.value, func(t *testing.T) {
			s := fixture(t)
			s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
			_, snap := sourceSkill(t, s, "example")
			r := sourceReceipt(snap.tree)
			writeSkillLock(t, filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), 3, map[string]skillReceipt{"example": r})
			s.skills()
			s.skillSources()
			src, err := SkillSourceForUpdate(s.Report.Items[0])
			if err != nil {
				t.Fatal(err)
			}
			ownPath := SkillReceiptPath(s.Config.Home, src.Root)
			data, err := json.Marshal(SkillUpdateReceipt{Schema: SkillUpdateReceiptSchema, Source: src, Commit: strings.Repeat("c", 40), Tree: snap.tree})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(ownPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(ownPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			// Decode pin evidence even if a typed identity field is invalid.
			data, err = json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]json.RawMessage
			if err = json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			record["pinnedRef"], record[tc.field] = json.RawMessage(`"main"`), json.RawMessage(tc.value)
			data, err = json.Marshal(map[string]any{"version": 3, "skills": map[string]any{"example": record}})
			if err != nil {
				t.Fatal(err)
			}
			xdg := filepath.Join(s.Config.Home, "state")
			t.Setenv("XDG_STATE_HOME", xdg)
			lockPath := filepath.Join(xdg, "skills/.skill-lock.json")
			write(t, lockPath, string(data))
			fresh := New(s.Config)
			fresh.skills()
			fresh.skillSources()
			if len(fresh.Report.Items) != 1 || fresh.Report.Items[0].skillSource != nil || fresh.Report.Items[0].Update.Code != "skill_source_unverified" {
				t.Fatal("invalid identity hid a canonical pin or own receipt restored source", fresh.Report.Items)
			}
			got, err := os.ReadFile(lockPath)
			if err != nil || string(got) != string(data) {
				t.Fatal("external lock changed", err)
			}
		})
	}
}
