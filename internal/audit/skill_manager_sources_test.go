package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shareEntry(t *testing.T, root string) skillshareEntry {
	t.Helper()
	snap, err := readSkillSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	for _, f := range snap.files {
		sum := sha256.Sum256(f.data)
		hashes[f.name] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return skillshareEntry{Source: "example/skills/skills/example", Type: "github-subdir", RepoURL: "https://github.com/example/skills.git", Subdir: "skills/example", Commit: strings.Repeat("a", 40), Version: strings.Repeat("a", 7), FileHashes: hashes}
}
func writeShare(t *testing.T, root string, entries map[string]skillshareEntry) string {
	t.Helper()
	p := filepath.Join(root, ".metadata.json")
	b, err := json.Marshal(map[string]any{"version": 1, "entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	write(t, p, string(b))
	return p
}
func shareItem(t *testing.T, s *Scanner, p string) Item {
	t.Helper()
	for _, i := range s.Report.Items {
		if i.Path == p {
			return i
		}
	}
	t.Fatalf("missing %s", p)
	return Item{}
}
func assertShare(t *testing.T, i Item, code string) {
	t.Helper()
	if i.SkillManager == nil || i.SkillManager.Code != code || i.Update.Status != "unknown" || i.Update.Code != code || i.skillSource != nil || i.Latest != "" {
		t.Fatalf("bad provenance: %+v / %+v", i, i.SkillManager)
	}
	if _, err := SkillSourceForUpdate(i); err == nil {
		t.Fatal("manager authorized copied update")
	}
}
func TestSkillshareCanonicalAliasesAndIndependentCopies(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "explicit-skills")
	s.Config.SkillRoots = []string{root}
	p := filepath.Join(root, "group/example/SKILL.md")
	write(t, p, "---\nname: example\n---\n")
	write(t, filepath.Join(filepath.Dir(p), "scripts/run.sh"), "echo example\n")
	e := shareEntry(t, filepath.Dir(p))
	e.Group = "group"
	metadata := writeShare(t, root, map[string]skillshareEntry{"example": e})
	alias := filepath.Join(s.Config.Home, ".codex/skills/renamed")
	link(t, filepath.Dir(p), alias)
	independent := filepath.Join(s.Config.Home, ".agents/skills/example/SKILL.md")
	write(t, independent, "---\nname: example\n---\n")
	before, _ := os.ReadFile(metadata)
	snapshot, _ := readSkillSnapshot(root)
	s.skills()
	s.skillSources()
	s.checkSkillSourcesWithGit(func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("ran git update check")
		return nil, nil
	})
	for _, path := range []string{p, filepath.Join(alias, "SKILL.md")} {
		i := shareItem(t, s, path)
		assertShare(t, i, "skillshare_managed")
		if i.SkillManager.Root != real(filepath.Dir(p)) || i.SkillManager.Repository != "example/skills" {
			t.Fatal(i.SkillManager)
		}
	}
	if shareItem(t, s, independent).SkillManager != nil {
		t.Fatal("name-only association")
	}
	after, _ := os.ReadFile(metadata)
	final, _ := readSkillSnapshot(root)
	if string(before) != string(after) || snapshot.tree != final.tree {
		t.Fatal("scan changed files")
	}
}
func TestSkillshareSameNameDifferentSources(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, ".agents/skills")
	entries := map[string]skillshareEntry{}
	for _, group := range []string{"one", "two"} {
		p := filepath.Join(root, group, "example/SKILL.md")
		write(t, p, "---\n---\n")
		e := shareEntry(t, filepath.Dir(p))
		e.Source = group + "/repo"
		e.RepoURL = "https://github.com/" + group + "/repo.git"
		e.Type = "github"
		e.Subdir = ""
		entries[group+"/example"] = e
	}
	writeShare(t, root, entries)
	s.skills()
	s.skillSources()
	if len(s.Report.Items) != 2 {
		t.Fatal(s.Report.Items)
	}
	for _, i := range s.Report.Items {
		assertShare(t, i, "skillshare_managed")
	}
	if s.Report.Items[0].SkillManager.Repository == s.Report.Items[1].SkillManager.Repository {
		t.Fatal("merged sources")
	}
}
func TestSkillshareUncertainEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*testing.T, string, *skillshareEntry)
	}{
		{"missing-baseline", "skillshare_baseline_missing", func(t *testing.T, r string, e *skillshareEntry) { e.FileHashes = nil }},
		{"missing-entry-hash", "skillshare_baseline_missing", func(t *testing.T, r string, e *skillshareEntry) {
			delete(e.FileHashes, "SKILL.md")
			e.FileHashes["other"] = "sha256:" + strings.Repeat("a", 64)
		}},
		{"modified", "skillshare_local_changes", func(t *testing.T, r string, e *skillshareEntry) { write(t, filepath.Join(r, "SKILL.md"), "changed") }},
		{"extra", "skillshare_local_changes", func(t *testing.T, r string, e *skillshareEntry) { write(t, filepath.Join(r, "extra"), "new") }},
		{"deleted", "skillshare_local_changes", func(t *testing.T, r string, e *skillshareEntry) {
			e.FileHashes["gone"] = "sha256:" + strings.Repeat("a", 64)
		}},
		{"symlink-payload", "skillshare_metadata_unverified", func(t *testing.T, r string, e *skillshareEntry) { link(t, "SKILL.md", filepath.Join(r, "linked")) }},
		{"escape-subdir", "skillshare_metadata_unverified", func(t *testing.T, r string, e *skillshareEntry) { e.Subdir = "../example" }},
		{"escape-hash", "skillshare_metadata_unverified", func(t *testing.T, r string, e *skillshareEntry) {
			e.FileHashes["../outside"] = "sha256:" + strings.Repeat("a", 64)
		}},
		{"identity-conflict", "skillshare_metadata_unverified", func(t *testing.T, r string, e *skillshareEntry) { e.RepoURL = "https://github.com/other/repo" }},
		{"commit-conflict", "skillshare_metadata_unverified", func(t *testing.T, r string, e *skillshareEntry) { e.Version = strings.Repeat("b", 7) }},
		{"missing-commit", "skillshare_metadata_unverified", func(t *testing.T, r string, e *skillshareEntry) { e.Version = ""; e.Commit = "" }},
		{"tracked", "skillshare_metadata_unverified", func(t *testing.T, r string, e *skillshareEntry) { e.Tracked = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			p, _ := sourceSkill(t, s, "example")
			root := filepath.Dir(p)
			e := shareEntry(t, root)
			tc.change(t, root, &e)
			writeShare(t, filepath.Dir(root), map[string]skillshareEntry{"example": e})
			s.skills()
			s.skillSources()
			assertShare(t, shareItem(t, s, p), tc.code)
		})
	}
}
func TestSkillshareLegacyAndCentralConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatible", true: "conflict"}[conflict], func(t *testing.T) {
			s := fixture(t)
			p, _ := sourceSkill(t, s, "example")
			root := filepath.Dir(p)
			e := shareEntry(t, root)
			b, _ := json.Marshal(e)
			legacy := filepath.Join(root, ".skillshare-meta.json")
			write(t, legacy, string(b))
			s.skills()
			s.skillSources()
			assertShare(t, shareItem(t, s, p), "skillshare_managed")
			if conflict {
				e.Commit = strings.Repeat("b", 40)
				e.Version = ""
			}
			central := writeShare(t, filepath.Dir(root), map[string]skillshareEntry{"example": e})
			s = New(s.Config)
			s.skills()
			s.skillSources()
			code := "skillshare_managed"
			if conflict {
				code = "skill_source_conflict"
			}
			i := shareItem(t, s, p)
			assertShare(t, i, code)
			if !conflict && real(i.SkillManager.MetadataPath) != real(central) {
				t.Fatal("legacy preferred over central")
			}
			after, _ := os.ReadFile(legacy)
			if string(after) != string(b) {
				t.Fatal("migrated legacy")
			}
		})
	}
}
func TestSkillshareUnknownSchemaAndEscapedKeys(t *testing.T) {
	for _, body := range []string{`{"version":2,"entries":{}}`, `{"version":1,"entries":{},"unknown":true}`, `{"version":1,"entries":{"../escape":{}}}`, `{"version":1,"entries":{"example":{"source":"example/repo","future":true}}}`, `not json`} {
		s := fixture(t)
		p, _ := sourceSkill(t, s, "example")
		write(t, filepath.Join(filepath.Dir(filepath.Dir(p)), ".metadata.json"), body)
		s.skills()
		s.skillSources()
		assertShare(t, shareItem(t, s, p), "skillshare_metadata_unverified")
	}
}
func TestSkillshareCannotReuseVercelOrAppAuthority(t *testing.T) {
	s := fixture(t)
	p, snap := sourceSkill(t, s, "example")
	root := filepath.Dir(p)
	lock := filepath.Join(s.Config.Home, ".agents/.skill-lock.json")
	writeSkillLock(t, lock, 3, map[string]skillReceipt{"example": sourceReceipt(snap.tree)})
	s.skills()
	s.skillSources()
	src, err := SkillSourceForUpdate(shareItem(t, s, p))
	if err != nil {
		t.Fatal(err)
	}
	receipt := SkillUpdateReceipt{Schema: SkillUpdateReceiptSchema, Source: src, Commit: strings.Repeat("c", 40), Tree: snap.tree}
	b, _ := json.Marshal(receipt)
	rp := SkillReceiptPath(s.Config.Home, root)
	write(t, rp, string(b))
	os.Chmod(filepath.Dir(rp), 0700)
	os.Chmod(rp, 0600)
	writeShare(t, filepath.Dir(root), map[string]skillshareEntry{"example": shareEntry(t, root)})
	if VerifySkillUpdateSource(src, s.Config.Home) == nil {
		t.Fatal("saved source bypassed foreign owner")
	}
	s = New(s.Config)
	s.skills()
	s.skillSources()
	assertShare(t, shareItem(t, s, p), "skill_source_conflict")
	pinned := sourceReceipt(snap.tree)
	pinned.PinnedRef = json.RawMessage(`"v1"`)
	writeSkillLock(t, lock, 3, map[string]skillReceipt{"example": pinned})
	s = New(s.Config)
	s.skills()
	s.skillSources()
	assertShare(t, shareItem(t, s, p), "skill_source_unverified")
}

func TestSkillshareEscapedAndChangedAliases(t *testing.T) {
	t.Run("escaped", func(t *testing.T) {
		s := fixture(t)
		outside := filepath.Join(s.Config.Home, "outside")
		write(t, filepath.Join(outside, "SKILL.md"), "---\n---\n")
		root := filepath.Join(s.Config.Home, ".agents/skills")
		alias := filepath.Join(root, "example")
		link(t, outside, alias)
		writeShare(t, root, map[string]skillshareEntry{"example": shareEntry(t, outside)})
		s.skills()
		s.skillSources()
		assertShare(t, shareItem(t, s, filepath.Join(alias, "SKILL.md")), "skillshare_metadata_unverified")
	})
	t.Run("changed-during-scan", func(t *testing.T) {
		s := fixture(t)
		p, _ := sourceSkill(t, s, "example")
		root := filepath.Dir(p)
		writeShare(t, filepath.Dir(root), map[string]skillshareEntry{"example": shareEntry(t, root)})
		alias := filepath.Join(s.Config.Home, ".codex/skills/alias")
		link(t, root, alias)
		s.skills()
		outside := filepath.Join(s.Config.Home, "outside")
		write(t, filepath.Join(outside, "SKILL.md"), "---\n---\n")
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		link(t, outside, alias)
		s.skillSources()
		i := shareItem(t, s, filepath.Join(alias, "SKILL.md"))
		assertShare(t, i, "skillshare_metadata_unverified")
		if i.SkillManager.Root != real(outside) || i.SkillManager.Repository != "" || i.RealPath != real(filepath.Join(outside, "SKILL.md")) {
			t.Fatal("retargeted alias retained old source identity", i)
		}
	})
}

func TestSkillshareMetadataLinkIsNotEvidence(t *testing.T) {
	s := fixture(t)
	p, _ := sourceSkill(t, s, "example")
	root := filepath.Dir(p)
	outside := filepath.Join(s.Config.Home, "outside")
	file := writeShare(t, outside, map[string]skillshareEntry{"example": shareEntry(t, root)})
	link(t, file, filepath.Join(filepath.Dir(root), ".metadata.json"))
	s.skills()
	s.skillSources()
	assertShare(t, shareItem(t, s, p), "skillshare_metadata_unverified")
}

func TestSkillshareMissingMetadataDoesNotInferOwnership(t *testing.T) {
	s := fixture(t)
	p, _ := sourceSkill(t, s, "skillshare")
	s.skills()
	s.skillSources()
	i := shareItem(t, s, p)
	if i.SkillManager != nil || i.Update.Status == "current" {
		t.Fatal(i)
	}
}

func TestSkillshareSourceFormsMustAgreeWithGitHubRepository(t *testing.T) {
	for _, tc := range []struct {
		source   string
		verified bool
	}{
		{"example/skills/skills/example", true},
		{"github.com/example/skills/skills/example", true},
		{"https://github.com/example/skills/skills/example", true},
		{"https://example/skills/skills/example", false},
		{"https://github.com.example.test/example/skills/skills/example", false},
		{"https://user@github.com/example/skills/skills/example", false},
		{"https://github.com/example/skills/skills/example?ref=main", false},
		{"https://github.com/example/skills/skills/example#main", false},
		{"http://github.com/example/skills/skills/example", false},
		{"https://github.com/example/skills/skills%2Fexample", false},
		{"https://github.com/other/skills/skills/example", false},
	} {
		t.Run(tc.source, func(t *testing.T) {
			s := fixture(t)
			p, _ := sourceSkill(t, s, "example")
			e := shareEntry(t, filepath.Dir(p))
			e.Source = tc.source
			writeShare(t, filepath.Dir(filepath.Dir(p)), map[string]skillshareEntry{"example": e})
			s.skills()
			s.skillSources()
			i := shareItem(t, s, p)
			code := "skillshare_metadata_unverified"
			if tc.verified {
				code = "skillshare_managed"
			}
			assertShare(t, i, code)
			if !tc.verified && i.SkillManager.Repository != "" {
				t.Fatal("contradictory URL retained repository identity", i.SkillManager)
			}
		})
	}
}

func TestSkillshareEntryLinkDoesNotVerifyIndependentSiblingPayload(t *testing.T) {
	s := fixture(t)
	p, _ := sourceSkill(t, s, "example")
	write(t, filepath.Join(filepath.Dir(p), "payload.txt"), "canonical")
	writeShare(t, filepath.Dir(filepath.Dir(p)), map[string]skillshareEntry{"example": shareEntry(t, filepath.Dir(p))})
	alias := filepath.Join(s.Config.Home, ".codex/skills/entry-only")
	link(t, p, filepath.Join(alias, "SKILL.md"))
	write(t, filepath.Join(alias, "payload.txt"), "independent local edit")
	s.skills()
	s.skillSources()
	assertShare(t, shareItem(t, s, p), "skillshare_managed")
	i := shareItem(t, s, filepath.Join(alias, "SKILL.md"))
	assertShare(t, i, "skillshare_metadata_unverified")
	if i.SkillManager.Root != real(alias) || i.SkillManager.Repository != "" {
		t.Fatal(i.SkillManager)
	}
}

func TestSkillshareLegacyRejectsRegistryOnlyFields(t *testing.T) {
	for _, field := range []string{"group", "into", "tracked"} {
		t.Run(field, func(t *testing.T) {
			s := fixture(t)
			p, _ := sourceSkill(t, s, "example")
			root := filepath.Dir(p)
			b, _ := json.Marshal(shareEntry(t, root))
			var record map[string]any
			if err := json.Unmarshal(b, &record); err != nil {
				t.Fatal(err)
			}
			record[field] = nil
			b, _ = json.Marshal(record)
			write(t, filepath.Join(root, ".skillshare-meta.json"), string(b))
			s.skills()
			s.skillSources()
			assertShare(t, shareItem(t, s, p), "skillshare_metadata_unverified")
		})
	}
}
