package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// SkillUpdateSource is a validated local receipt, not permission to overwrite it.
type SkillUpdateSource struct {
	Repo          string `json:"repo"`
	Ref           string `json:"ref"`
	Entry         string `json:"entry"`
	Root          string `json:"root"`
	Baseline      string `json:"baseline"`
	HashKind      string `json:"hash_kind"`
	SourcePath    string `json:"source_path"`
	SourceName    string `json:"source_name"`
	SourceKind    string `json:"source_kind"`
	SourceDigest  string `json:"source_digest"`
	ReceiptDigest string `json:"receipt_digest,omitempty"`
}
type SkillUpdateFile struct {
	Path, Mode, SHA string
	Data            []byte
}
type SkillUpdateSnapshot struct {
	Commit string
	Tree   string
	Files  []SkillUpdateFile
}
type SkillUpdateReceipt struct {
	Schema string            `json:"schema"`
	Source SkillUpdateSource `json:"source"`
	Commit string            `json:"commit"`
	Tree   string            `json:"tree"`
}

const SkillUpdateReceiptSchema = "keepupdated/skill-receipt/v1"

func SkillReceiptPath(home, root string) string {
	h := sha256.Sum256([]byte(root))
	return filepath.Join(home, "Library/Application Support/KeepUpdated/skill-receipts", hex.EncodeToString(h[:])+".json")
}
func sourceRecordDigest(r skillReceipt, kind string) string {
	b, _ := json.Marshal(struct {
		Record skillReceipt
		Kind   string
	}{r, kind})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func SkillSourceForUpdate(i Item) (SkillUpdateSource, error) {
	s := i.skillSource
	if i.SkillManager != nil || s == nil || i.Update.Code == "skill_source_conflict" || i.Update.Code == "skill_source_unverified" {
		return SkillUpdateSource{}, errors.New("skill_source_unverified")
	}
	return SkillUpdateSource{s.repo, s.ref, s.entry, s.root, s.baseline, s.hashKind, s.receiptPath, s.receiptName, s.receiptKind, s.receiptDigest, s.appReceiptDigest}, nil
}
func ReadSkillUpdateSnapshot(root string) (SkillUpdateSnapshot, error) {
	seenPaths := map[string]bool{}
	count := 0
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		count++
		if count > 4096 {
			return errors.New("skill_local_hash_limit")
		}
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		if !utf8.ValidString(rel) || strings.ContainsAny(rel, "\\\x00\r\n\t") {
			return errors.New("skill_unsupported_local_files")
		}
		key := strings.ToLower(filepath.ToSlash(rel))
		if seenPaths[key] {
			return errors.New("skill_case_collision")
		}
		seenPaths[key] = true
		if strings.EqualFold(d.Name(), ".git") || strings.EqualFold(d.Name(), "node_modules") {
			return errors.New("skill_unsupported_local_files")
		}
		return nil
	}); err != nil {
		return SkillUpdateSnapshot{}, err
	}
	s, e := readSkillSnapshot(root)
	if e != nil {
		return SkillUpdateSnapshot{}, e
	}
	if s.unhashed {
		return SkillUpdateSnapshot{}, errors.New("skill_unsupported_local_files")
	}
	out := SkillUpdateSnapshot{Tree: s.tree, Files: []SkillUpdateFile{}}
	seen := map[string]bool{}
	for _, f := range s.files {
		if !utf8.ValidString(f.name) || seen[strings.ToLower(f.name)] {
			return out, errors.New("skill_case_collision")
		}
		seen[strings.ToLower(f.name)] = true
		out.Files = append(out.Files, SkillUpdateFile{f.name, f.mode, f.hash, append([]byte(nil), f.data...)})
	}
	sort.Slice(out.Files, func(a, b int) bool { return out.Files[a].Path < out.Files[b].Path })
	return out, nil
}
func SkillSnapshotMatchesSource(s SkillUpdateSnapshot, src SkillUpdateSource) bool {
	original := skillSnapshot{tree: s.Tree}
	for _, f := range s.Files {
		original.files = append(original.files, skillFile{f.Path, f.Mode, f.SHA, f.Data})
	}
	return original.matchesBaseline(&skillSource{baseline: src.Baseline, hashKind: src.HashKind})
}
func validUpdateSource(src SkillUpdateSource) bool {
	r := skillReceipt{Source: src.Repo, SourceType: "github", Ref: src.Ref, SkillPath: src.Entry, FolderHash: src.Baseline, ComputedHash: src.Baseline}
	_, ok := parsedSkillSource(r, src.HashKind)
	return ok && src.Baseline != "" && (src.HashKind == "tree" || src.HashKind == "content") && filepath.IsAbs(src.Root) && filepath.Clean(src.Root) == src.Root && filepath.IsAbs(src.SourcePath) && filepath.Clean(src.SourcePath) == src.SourcePath && skillHex64.MatchString(src.SourceDigest) && (src.SourceKind == "tree" || src.SourceKind == "content") && src.SourceName != "" && !strings.ContainsAny(src.SourceName, "/\\\x00\r\n")
}
func VerifySkillUpdateSource(src SkillUpdateSource, home string) error {
	if !validUpdateSource(src) {
		return errors.New("skill_source_invalid")
	}
	// A saved app receipt cannot turn an arbitrary JSON path into source authority.
	allowedPath := src.SourcePath == filepath.Join(home, ".agents/.skill-lock.json")
	if src.SourceKind == "tree" && within(src.SourcePath, home) && filepath.Base(src.SourcePath) == ".skill-lock.json" && filepath.Base(filepath.Dir(src.SourcePath)) == "skills" {
		allowedPath = true
	}
	if src.SourceKind == "content" && within(src.SourcePath, home) && filepath.Base(src.SourcePath) == "skills-lock.json" && within(src.Root, filepath.Dir(src.SourcePath)) {
		allowedPath = true
	}
	if !allowedPath || src.SourceName == "." || src.SourceName == ".." {
		return errors.New("skill_source_invalid")
	}
	if len(skillshareRecords(src.Root)) > 0 || (real(src.Root) != "" && real(src.Root) != src.Root && len(skillshareRecords(real(src.Root))) > 0) {
		return errors.New("skillshare_managed")
	}
	b, e := readSmall(src.SourcePath)
	if e != nil {
		return errors.New("skill_source_receipt_unavailable")
	}
	var lock skillLock
	if json.Unmarshal(b, &lock) != nil || (src.SourceKind == "tree" && lock.Version != 3) || (src.SourceKind == "content" && lock.Version != 1) {
		return errors.New("skill_source_receipt_invalid")
	}
	r, ok := lock.Skills[src.SourceName]
	if !ok || sourceRecordDigest(r, src.SourceKind) != src.SourceDigest {
		return errors.New("skill_source_receipt_changed")
	}
	parsed, valid := parsedSkillSource(r, src.SourceKind)
	if !valid || parsed.repo != src.Repo || parsed.ref != src.Ref || parsed.entry != src.Entry {
		return errors.New("skill_source_receipt_changed")
	}
	if src.ReceiptDigest != "" {
		b, e = ReadOwnSkillReceipt(home, src.Root)
		if e != nil {
			return e
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != src.ReceiptDigest {
			return errors.New("skill_source_receipt_changed")
		}
	}
	return nil
}
func ReadOwnSkillReceipt(home, root string) ([]byte, error) {
	p := SkillReceiptPath(home, root)
	if _, e := os.Lstat(p); e != nil {
		return nil, e
	}
	dir := filepath.Dir(p)
	realDir, e := filepath.EvalSymlinks(dir)
	if e != nil || realDir != dir {
		return nil, errors.New("skill_receipt_directory_invalid")
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("skill_receipt_directory_invalid")
	}
	ss, ok := st.Sys().(*syscall.Stat_t)
	if !ok || ss.Uid != uint32(os.Getuid()) {
		return nil, errors.New("skill_receipt_owner_invalid")
	}
	st, e = os.Lstat(p)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("skill_receipt_file_invalid")
	}
	ss, ok = st.Sys().(*syscall.Stat_t)
	if !ok || ss.Uid != uint32(os.Getuid()) {
		return nil, errors.New("skill_receipt_owner_invalid")
	}
	b, e := readSmall(p)
	if e != nil {
		return nil, e
	}
	var r SkillUpdateReceipt
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Schema != SkillUpdateReceiptSchema || r.Source.Root != root || !validUpdateSource(r.Source) || !skillHex40.MatchString(r.Commit) || !skillHex40.MatchString(r.Tree) || r.Source.Baseline != r.Tree || r.Source.HashKind != "tree" {
		return nil, errors.New("skill_receipt_schema_invalid")
	}
	return b, nil
}
func (s *Scanner) ownSkillSources() {
	for n := range s.Report.Items {
		i := &s.Report.Items[n]
		if i.Kind != "skill" || i.Source != "local" || i.Owner == "system" || strings.Contains(i.Path, "/plugins/cache/") || i.Update.Code == "skill_source_unverified" {
			continue
		}
		rp := real(i.Path)
		if rp == "" {
			continue
		}
		root := filepath.Dir(rp)
		p := SkillReceiptPath(s.Config.Home, root)
		if _, e := os.Lstat(p); os.IsNotExist(e) {
			continue
		}
		b, e := ReadOwnSkillReceipt(s.Config.Home, root)
		if e != nil {
			i.skillSource = nil
			i.Update = result("unknown", "skill_source_conflict")
			continue
		}
		var receipt SkillUpdateReceipt
		_ = json.Unmarshal(b, &receipt)
		src := receipt.Source
		src.ReceiptDigest = ""
		if VerifySkillUpdateSource(src, s.Config.Home) != nil || i.Update.Code == "skill_source_conflict" {
			i.skillSource = nil
			i.Update = result("unknown", "skill_source_conflict")
			continue
		}
		if old := i.skillSource; old != nil && (old.repo != src.Repo || old.ref != src.Ref || old.entry != src.Entry || old.receiptDigest != src.SourceDigest) {
			i.skillSource = nil
			i.Update = result("unknown", "skill_source_conflict")
			continue
		}
		h := sha256.Sum256(b)
		i.skillSource = &skillSource{repo: src.Repo, ref: src.Ref, entry: src.Entry, root: root, baseline: src.Baseline, hashKind: "tree", receiptPath: src.SourcePath, receiptName: src.SourceName, receiptKind: src.SourceKind, receiptDigest: src.SourceDigest, appReceiptDigest: hex.EncodeToString(h[:])}
		i.Update = result("unknown", "online_query_required")
		i.Evidence = append(i.Evidence, "skill-receipt:"+p, "skill-manager:keepupdated")
	}
}

// DownloadSkillUpdate resolves the receipt ref when both pins are empty, then
// verifies every selected Git object. Nonempty pins must match exactly.
// The caller supplies only a trusted transport for tests, never plan-controlled URLs.
func DownloadSkillUpdate(ctx context.Context, src SkillUpdateSource, commit, expectedTree string, client *http.Client) (SkillUpdateSnapshot, error) {
	return DownloadSkillUpdateCached(ctx, src, commit, expectedTree, client, "")
}

// cacheDir is trusted application configuration, never read from a plan. Immutable
// Git objects are hash-verified on every read; the mutable ref is always queried.
func DownloadSkillUpdateCached(ctx context.Context, src SkillUpdateSource, commit, expectedTree string, client *http.Client, cacheDir string) (SkillUpdateSnapshot, error) {
	session := NewSkillUpdateDownloadSession()
	defer session.Close()
	return session.Download(ctx, src, commit, expectedTree, client, cacheDir)
}
func downloadSkillUpdateCachedWithGit(ctx context.Context, src SkillUpdateSource, commit, expectedTree string, client *http.Client, cacheDir string, runner skillGitRunner) (SkillUpdateSnapshot, error) {
	session := newSkillUpdateDownloadSession(runner)
	defer session.Close()
	return session.Download(ctx, src, commit, expectedTree, client, cacheDir)
}
func (session *SkillUpdateDownloadSession) Download(ctx context.Context, src SkillUpdateSource, commit, expectedTree string, client *http.Client, cacheDir string) (out SkillUpdateSnapshot, err error) {
	out = SkillUpdateSnapshot{Files: []SkillUpdateFile{}}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if err != nil && err.Error() == skillRateLimitCode {
			pin, tree := commit, expectedTree
			if out.Commit != "" {
				pin = out.Commit
			}
			if out.Tree != "" {
				tree = out.Tree
			}
			out, err = session.gitSnapshot(ctx, src, pin, tree, cacheDir)
		}
	}()

	if cacheDir != "" {
		if e := trustedSkillObjectCache(cacheDir); e != nil {
			return out, e
		}
	}
	if !validUpdateSource(src) || (commit == "") != (expectedTree == "") || commit != "" && (!skillHex40.MatchString(commit) || !skillHex40.MatchString(expectedTree)) {
		return out, errors.New("skill_upstream_unverified")
	}
	if session.limited[src.Repo+"\x00"+src.Ref] {
		return session.gitSnapshot(ctx, src, commit, expectedTree, cacheDir)
	}
	if client == nil {
		client = publicClient(25 * time.Second)
	}
	// Redirects, cookies, credentials and proxy configuration are not needed for GitHub objects.
	c := *client
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("skill_redirect_rejected") }
	get := func(endpoint string, v any) error {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+src.Repo+endpoint, nil)
		if e != nil {
			return e
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "KeepUpdated")
		resp, e := c.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if skillAPIRateLimited(resp) {
			return errors.New(skillRateLimitCode)
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("skill_download_http_%d", resp.StatusCode)
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
		if e != nil || len(b) > metadataLimit {
			return errors.New("skill_download_limit")
		}
		if json.Unmarshal(b, v) != nil {
			return errors.New("skill_download_invalid")
		}
		return nil
	}
	var cp struct {
		SHA    string `json:"sha"`
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	ref := commit
	if ref == "" {
		ref = src.Ref
		if ref == "" {
			ref = "HEAD"
		}
	}
	if e := get("/commits/"+url.PathEscape(ref), &cp); e != nil {
		return out, e
	}
	if !skillHex40.MatchString(cp.SHA) || !skillHex40.MatchString(cp.Commit.Tree.SHA) || skillHex40.MatchString(ref) && cp.SHA != ref {
		return out, errors.New("skill_commit_mismatch")
	}
	out.Commit = cp.SHA
	cache := map[string]skillTree{}
	var treeAt func(string) (skillTree, error)
	treeAt = func(sha string) (skillTree, error) {
		if t, ok := cache[sha]; ok {
			return t, nil
		}
		var t skillTree
		if !skillHex40.MatchString(sha) {
			return t, errors.New("skill_tree_invalid")
		}
		cached, hit, e := readSkillObjectCache(cacheDir, "tree-"+sha)
		if e != nil {
			return t, e
		}
		if hit {
			if json.Unmarshal(cached, &t) != nil {
				return t, errors.New("skill_object_cache_changed")
			}
		} else if e := get("/git/trees/"+sha, &t); e != nil {
			return t, e
		}
		if t.SHA != sha || t.Truncated || len(t.Tree) > 4096 {
			return t, errors.New("skill_tree_incomplete")
		}
		entries := append([]skillTreeEntry(nil), t.Tree...)
		seen := map[string]bool{}
		for _, x := range entries {
			if x.Path == "" || x.Path == "." || x.Path == ".." || strings.ContainsAny(x.Path, "/\\\x00") || seen[x.Path] || !skillHex40.MatchString(x.SHA) || !((x.Type == "tree" && x.Mode == "040000") || (x.Type == "blob" && (x.Mode == "100644" || x.Mode == "100755" || x.Mode == "120000")) || (x.Type == "commit" && x.Mode == "160000")) {
				return t, errors.New("skill_tree_unsupported_file")
			}
			seen[x.Path] = true
		}
		sort.Slice(entries, func(a, b int) bool {
			an, bn := entries[a].Path, entries[b].Path
			if entries[a].Type == "tree" {
				an += "/"
			}
			if entries[b].Type == "tree" {
				bn += "/"
			}
			return an < bn
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
		if skillObject("tree", data) != sha {
			return t, errors.New("skill_tree_hash_mismatch")
		}
		if !hit {
			data, _ := json.Marshal(t)
			if e := writeSkillObjectCache(cacheDir, "tree-"+sha, data); e != nil {
				return t, e
			}
		}
		cache[sha] = t
		return t, nil
	}
	tree, e := treeAt(cp.Commit.Tree.SHA)
	if e != nil {
		return out, e
	}
	folder := path.Dir(src.Entry)
	if folder != "." {
		for _, segment := range strings.Split(folder, "/") {
			found := ""
			for _, x := range tree.Tree {
				if x.Path == segment && x.Type == "tree" {
					found = x.SHA
				}
			}
			if found == "" {
				return out, errors.New("skill_upstream_path_missing")
			}
			tree, e = treeAt(found)
			if e != nil {
				return out, e
			}
		}
	}
	if expectedTree != "" && tree.SHA != expectedTree {
		return out, errors.New("skill_upstream_tree_changed")
	}
	out.Tree = tree.SHA
	total, count := 0, 0
	var walk func(skillTree, string, int) error
	walk = func(t skillTree, prefix string, depth int) error {
		if depth > 14 {
			return errors.New("skill_tree_depth_limit")
		}
		seen := map[string]bool{}
		for _, x := range t.Tree {
			if strings.EqualFold(x.Path, ".git") || strings.EqualFold(x.Path, "node_modules") || strings.ContainsAny(x.Path, "\\\x00\r\n\t") || !utf8.ValidString(x.Path) || seen[strings.ToLower(x.Path)] || !((x.Type == "tree" && x.Mode == "040000") || (x.Type == "blob" && (x.Mode == "100644" || x.Mode == "100755"))) {
				return errors.New("skill_tree_unsupported_file")
			}
			seen[strings.ToLower(x.Path)] = true
			count++
			if count > 4096 {
				return errors.New("skill_tree_entry_limit")
			}
			name := prefix + x.Path
			if x.Type == "tree" {
				next, e := treeAt(x.SHA)
				if e != nil {
					return e
				}
				if e = walk(next, name+"/", depth+1); e != nil {
					return e
				}
				continue
			}
			data, hit, e := readSkillObjectCache(cacheDir, "blob-"+x.SHA)
			if e != nil {
				return e
			}
			if !hit {
				var blob struct {
					SHA      string `json:"sha"`
					Encoding string `json:"encoding"`
					Content  string `json:"content"`
					Size     int    `json:"size"`
				}
				if e := get("/git/blobs/"+x.SHA, &blob); e != nil {
					return e
				}
				data, e = decodeSkillBlob(blob.Encoding, blob.Content)
				if e != nil || blob.SHA != x.SHA || blob.Size != len(data) {
					return errors.New("skill_blob_hash_mismatch")
				}
			}
			if skillObject("blob", data) != x.SHA {
				return errors.New("skill_blob_hash_mismatch")
			}
			if !hit {
				if e = writeSkillObjectCache(cacheDir, "blob-"+x.SHA, data); e != nil {
					return e
				}
			}
			total += len(data)
			if total > metadataLimit {
				return errors.New("skill_download_limit")
			}
			out.Files = append(out.Files, SkillUpdateFile{name, x.Mode, x.SHA, data})
		}
		return nil
	}
	if e = walk(tree, "", 0); e != nil {
		return out, e
	}
	entry := false
	for _, f := range out.Files {
		if f.Path == "SKILL.md" {
			entry = true
		}
	}
	if !entry {
		return out, errors.New("skill_upstream_path_missing")
	}
	out.Tree = tree.SHA
	sort.Slice(out.Files, func(a, b int) bool { return out.Files[a].Path < out.Files[b].Path })
	return out, nil
}

func decodeSkillBlob(encoding, content string) ([]byte, error) {
	if encoding != "base64" {
		return nil, errors.New("skill_blob_encoding_invalid")
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(content, "\n", ""))
}

func trustedSkillObjectCache(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("skill_object_cache_invalid")
	}
	rp, e := filepath.EvalSymlinks(dir)
	if e != nil || rp != dir {
		return errors.New("skill_object_cache_invalid")
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("skill_object_cache_invalid")
	}
	ss, ok := st.Sys().(*syscall.Stat_t)
	if !ok || ss.Uid != uint32(os.Getuid()) {
		return errors.New("skill_object_cache_invalid")
	}
	return nil
}
func readSkillObjectCache(dir, name string) ([]byte, bool, error) {
	if dir == "" {
		return nil, false, nil
	}
	if e := trustedSkillObjectCache(dir); e != nil {
		return nil, false, e
	}
	p := filepath.Join(dir, name)
	st, e := os.Lstat(p)
	if os.IsNotExist(e) {
		return nil, false, nil
	}
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, false, errors.New("skill_object_cache_changed")
	}
	ss, ok := st.Sys().(*syscall.Stat_t)
	if !ok || ss.Uid != uint32(os.Getuid()) || ss.Nlink != 1 {
		return nil, false, errors.New("skill_object_cache_changed")
	}
	b, e := readSmall(p)
	if e != nil {
		return nil, false, errors.New("skill_object_cache_changed")
	}
	return b, true, nil
}
func writeSkillObjectCache(dir, name string, data []byte) error {
	if dir == "" {
		return nil
	}
	if e := trustedSkillObjectCache(dir); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".object-")
	if e != nil {
		return errors.New("skill_object_cache_unavailable")
	}
	p := f.Name()
	defer os.Remove(p)
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return errors.New("skill_object_cache_unavailable")
	}
	if e = os.Rename(p, filepath.Join(dir, name)); e != nil {
		return errors.New("skill_object_cache_unavailable")
	}
	return nil
}
