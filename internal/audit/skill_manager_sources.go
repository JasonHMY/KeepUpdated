package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// SkillManagerSource is read-only provenance, never a copied-update source.
type SkillManagerSource struct {
	Manager      string `json:"manager"`
	Repository   string `json:"repository,omitempty"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Root         string `json:"root"`
	MetadataPath string `json:"metadata_path"`
	Format       string `json:"format"`
	Code         string `json:"code"`
}

type skillshareEntry struct {
	Source      string            `json:"source"`
	Kind        string            `json:"kind,omitempty"`
	Type        string            `json:"type,omitempty"`
	Tracked     bool              `json:"tracked,omitempty"`
	Group       string            `json:"group,omitempty"`
	Branch      string            `json:"branch,omitempty"`
	Into        string            `json:"into,omitempty"`
	InstalledAt string            `json:"installed_at,omitempty"`
	RepoURL     string            `json:"repo_url,omitempty"`
	Subdir      string            `json:"subdir,omitempty"`
	Version     string            `json:"version,omitempty"`
	TreeHash    string            `json:"tree_hash,omitempty"`
	Commit      string            `json:"commit,omitempty"`
	FileHashes  map[string]string `json:"file_hashes,omitempty"`
	Layout      string            `json:"layout,omitempty"`
}

type skillshareRecord struct {
	entry        skillshareEntry
	file, format string
	invalid      bool
}

func strictSkillshareJSON(b []byte, target any) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(target) == nil && d.Decode(new(any)) == io.EOF
}

func skillshareRelative(p string) bool {
	return p != "" && p != "." && len(p) <= 4096 && strings.Count(p, "/") <= 14 && path.Clean(p) == p && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\r\n\t%?#")
}

func readSkillshareMetadata(file string) ([]byte, error) {
	st, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	return readSmall(file)
}

// Read only named metadata files on an already discovered directory's ancestors.
// No manager helper is safe here: upstream LoadMetadata can migrate and write.
func skillshareRecords(root string) []skillshareRecord {
	var records []skillshareRecord
	legacy := filepath.Join(root, ".skillshare-meta.json")
	if b, err := readSkillshareMetadata(legacy); !os.IsNotExist(err) {
		r := skillshareRecord{file: legacy, format: "legacy"}
		r.invalid = err != nil || !strictSkillshareJSON(b, &r.entry)
		// Registry-only fields are not part of the legacy sidecar schema.
		var registryFields struct{ Group, Into, Tracked json.RawMessage }
		if json.Unmarshal(b, &registryFields) != nil || len(registryFields.Group) != 0 || len(registryFields.Into) != 0 || len(registryFields.Tracked) != 0 {
			r.invalid = true
		}
		records = append(records, r)
	}
	for dir, depth := filepath.Dir(root), 0; dir != filepath.Dir(dir) && depth < 14; dir, depth = filepath.Dir(dir), depth+1 {
		file := filepath.Join(dir, ".metadata.json")
		b, err := readSkillshareMetadata(file)
		if os.IsNotExist(err) {
			continue
		}
		var store struct {
			Version         int                        `json:"version"`
			Entries         map[string]json.RawMessage `json:"entries"`
			AuditAccepted   map[string][]string        `json:"audit_accepted,omitempty"`
			TargetOverrides map[string][]string        `json:"target_overrides,omitempty"`
		}
		if err != nil || !strictSkillshareJSON(b, &store) || store.Version != 1 || store.Entries == nil {
			records = append(records, skillshareRecord{file: file, format: "central-v1", invalid: true})
			continue
		}
		// Inspect keys in stable order; conflicting records never pick a winner.
		keys := make([]string, 0, len(store.Entries))
		for key := range store.Entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			r := skillshareRecord{file: file, format: "central-v1"}
			r.invalid = !strictSkillshareJSON(store.Entries[key], &r.entry)
			rel := key
			if r.entry.Group != "" && !strings.HasPrefix(key, r.entry.Group+"/") {
				rel = r.entry.Group + "/" + key
			}
			if !skillshareRelative(rel) || (r.entry.Group != "" && !skillshareRelative(r.entry.Group)) {
				records = append(records, skillshareRecord{file: file, format: "central-v1", invalid: true})
				continue
			}
			target := filepath.Join(dir, filepath.FromSlash(rel))
			// Compare lexical paths as well as real paths so an escaped link stays manual.
			if target != root && real(target) != real(root) {
				continue
			}
			if !within(target, dir) || real(target) != filepath.Join(real(dir), filepath.FromSlash(rel)) {
				r.invalid = true
			}
			records = append(records, r)
		}
	}
	return records
}

// This deliberately supports only unambiguous public GitHub source forms.
func skillshareIdentity(e skillshareEntry) (repo, commit string, ok bool) {
	if e.Tracked || e.Layout != "" || (e.Kind != "" && e.Kind != "skill") || (e.Type != "github" && e.Type != "github-subdir") || !validSkillRef(e.Branch) {
		return
	}
	if e.Subdir != "" && !skillshareRelative(e.Subdir) {
		return
	}
	if (e.Type == "github-subdir") != (e.Subdir != "") {
		return
	}
	repo = strings.TrimSuffix(strings.TrimPrefix(e.RepoURL, "https://github.com/"), ".git")
	if !skillRepo.MatchString(repo) || strings.Contains(repo, "..") || (e.RepoURL != "https://github.com/"+repo && e.RepoURL != "https://github.com/"+repo+".git") {
		return "", "", false
	}
	raw := e.Source
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return "", "", false
		}
		raw = strings.TrimPrefix(u.Path, "/")
	} else {
		raw = strings.TrimPrefix(raw, "github.com/")
	}
	expected := repo
	if e.Subdir != "" {
		expected += "/" + e.Subdir
	}
	if raw != expected && !(e.Subdir == "" && raw == repo+".git") {
		return "", "", false
	}
	commit = e.Commit
	if commit == "" {
		commit = e.Version
	}
	if !skillHex40.MatchString(commit) || (e.Version != "" && (len(e.Version) < 7 || !strings.HasPrefix(commit, e.Version))) || (e.TreeHash != "" && !skillHex40.MatchString(e.TreeHash)) {
		return "", "", false
	}
	return repo, commit, true
}

func skillshareBaseline(root string, hashes map[string]string) string {
	if len(hashes) == 0 {
		return "skillshare_baseline_missing"
	}
	if _, ok := hashes["SKILL.md"]; !ok {
		return "skillshare_baseline_missing"
	}
	for name, hash := range hashes {
		if !skillshareRelative(name) || !strings.HasPrefix(hash, "sha256:") || !skillHex64.MatchString(strings.TrimPrefix(hash, "sha256:")) {
			return "skillshare_metadata_unverified"
		}
	}
	snapshot, err := readSkillSnapshot(root)
	if err != nil || snapshot.unhashed {
		return "skillshare_metadata_unverified"
	}
	actual := map[string]string{}
	for _, file := range snapshot.files {
		if file.name == ".skillshare-meta.json" {
			continue
		}
		sum := sha256.Sum256(file.data)
		actual[file.name] = "sha256:" + hex.EncodeToString(sum[:])
	}
	if !reflect.DeepEqual(actual, hashes) {
		return "skillshare_local_changes"
	}
	return "skillshare_managed"
}

func readSkillshareSource(root string, aliases ...string) *SkillManagerSource {
	records := skillshareRecords(root)
	for _, alias := range aliases {
		if alias != root {
			records = append(records, skillshareRecords(alias)...)
		}
	}
	if len(records) == 0 {
		return nil
	}
	p := &SkillManagerSource{Manager: "skillshare", Root: root, MetadataPath: records[0].file, Format: records[0].format, Code: "skillshare_metadata_unverified"}
	var baseline map[string]string
	var branch, tree string
	valid := true
	for n, r := range records {
		repo, commit, ok := skillshareIdentity(r.entry)
		if r.invalid || !ok {
			valid = false
			continue
		}
		if p.Repository != "" && (p.Repository != repo || p.Subdirectory != r.entry.Subdir || p.Commit != commit || !reflect.DeepEqual(baseline, r.entry.FileHashes) || branch != r.entry.Branch || (tree != "" && r.entry.TreeHash != "" && tree != r.entry.TreeHash)) {
			p.Code = "skill_source_conflict"
			p.Repository, p.Subdirectory, p.Commit = "", "", ""
			return p
		}
		p.Repository, p.Subdirectory, p.Commit = repo, r.entry.Subdir, commit
		baseline = r.entry.FileHashes
		branch, tree = r.entry.Branch, r.entry.TreeHash
		if n == 0 || r.format == "central-v1" {
			p.MetadataPath, p.Format = r.file, r.format
		}
	}
	if valid {
		p.Code = skillshareBaseline(root, baseline)
	}
	return p
}

func (s *Scanner) skillManagerSources() {
	aliases := map[string][]string{}
	for _, i := range s.Report.Items {
		if i.Kind != "skill" || i.Owner == "system" || i.Source == "plugin-cache" || strings.Contains(i.Path, "/plugins/cache/") {
			continue
		}
		if rp := real(i.Path); rp != "" {
			root := filepath.Dir(rp)
			aliases[root] = append(aliases[root], filepath.Dir(i.Path))
		}
	}
	sources := map[string]*SkillManagerSource{}
	for root, paths := range aliases {
		sources[root] = readSkillshareSource(root, uniq(paths)...)
	}
	// A conflicting record on any discovered alias affects the shared directory.
	for _, i := range s.Report.Items {
		p := sources[filepath.Dir(real(i.Path))]
		if p != nil && (i.skillSource != nil || i.Update.Code == "skill_source_conflict") {
			p.Code = "skill_source_conflict"
			p.Repository, p.Subdirectory, p.Commit = "", "", ""
		}
	}
	for n := range s.Report.Items {
		i := &s.Report.Items[n]
		if i.Kind != "skill" || i.Owner == "system" || i.Source == "plugin-cache" || strings.Contains(i.Path, "/plugins/cache/") {
			continue
		}
		rp := real(i.Path)
		if rp == "" {
			continue
		}
		p := sources[filepath.Dir(rp)]
		if i.RealPath != "" && i.RealPath != rp {
			if p == nil {
				p = readSkillshareSource(filepath.Dir(i.RealPath))
			}
			if p != nil {
				copy := *p
				copy.Code = "skillshare_metadata_unverified"
				copy.Root = filepath.Dir(rp)
				copy.Repository, copy.Subdirectory, copy.Commit = "", "", ""
				i.RealPath = rp
				p = &copy
			}
		}
		if p == nil {
			continue
		}
		copy := *p
		// A linked entry file does not make its sibling payload a directory alias.
		if directory := real(filepath.Dir(i.Path)); directory != filepath.Dir(rp) {
			copy.Code = "skillshare_metadata_unverified"
			copy.Root = directory
			copy.Repository, copy.Subdirectory, copy.Commit = "", "", ""
		}
		i.SkillManager = &copy
		// Preserve first-batch pins and pre-existing conflicting authority.
		if i.Update.Code == "skill_source_conflict" || i.Update.Code == "skill_source_unverified" {
			i.SkillManager.Code = i.Update.Code
		}
		i.skillSource = nil
		i.Update = result("unknown", i.SkillManager.Code)
		i.Latest = ""
		i.Evidence = append(i.Evidence, "skill-manager:skillshare", "skill-manager-metadata:"+p.MetadataPath)
	}
}
