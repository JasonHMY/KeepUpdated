package audit

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
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
	"regexp"
	"sort"
	"strings"
)

// Receipts identify a source; they never authorize replacing a copied Skill.
type skillSource struct {
	repo, ref, entry, baseline, hashKind                                   string
	root                                                                   string
	receiptPath, receiptName, receiptKind, receiptDigest, appReceiptDigest string
}
type skillReceipt struct {
	Source       string          `json:"source"`
	SourceType   string          `json:"sourceType"`
	SourceURL    string          `json:"sourceUrl"`
	Ref          string          `json:"ref"`
	SkillPath    string          `json:"skillPath"`
	FolderHash   string          `json:"skillFolderHash"`
	ComputedHash string          `json:"computedHash"`
	PinnedRef    json.RawMessage `json:"pinnedRef,omitempty"`
}
type skillLock struct {
	Version int                     `json:"version"`
	Skills  map[string]skillReceipt `json:"skills"`
}

var skillRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var skillHex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var skillHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validSkillRef(ref string) bool {
	return len(ref) <= 200 && !strings.Contains(ref, "..") && !strings.ContainsAny(ref, "\\?#%:@{} ~^*[]\x00\r\n\t") && !strings.HasPrefix(ref, "/") && !strings.HasSuffix(ref, "/") && !strings.Contains(ref, "//")
}

// Unknown pin semantics stay manual, including null and non-string values.
func unsupportedSkillPin(r skillReceipt) bool {
	if len(r.PinnedRef) == 0 {
		return false
	}
	var pin *string
	return json.Unmarshal(r.PinnedRef, &pin) != nil || pin == nil || *pin != ""
}

func parsedSkillSource(r skillReceipt, kind string) (*skillSource, bool) {
	if unsupportedSkillPin(r) {
		return nil, false
	}
	if r.SourceType != "github" || len(r.Source) > 200 || !skillRepo.MatchString(r.Source) || !validSkillRef(r.Ref) || strings.Contains(r.Source, "..") {
		return nil, false
	}
	if r.SourceURL != "" {
		u, err := url.Parse(r.SourceURL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git") != r.Source {
			return nil, false
		}
	}
	if r.SkillPath == "" || len(r.SkillPath) > 4096 || strings.Count(r.SkillPath, "/") > 14 || path.Clean(r.SkillPath) != r.SkillPath || path.IsAbs(r.SkillPath) || strings.HasPrefix(r.SkillPath, "../") || strings.ContainsAny(r.SkillPath, "\\\x00\r\n") || path.Base(r.SkillPath) != "SKILL.md" {
		return nil, false
	}
	h := r.FolderHash
	if kind == "content" {
		h = r.ComputedHash
	} else if skillHex64.MatchString(h) {
		// Global receipts from the CLI clone fallback use content SHA-256.
		kind = "content"
	}
	if h != "" && (kind == "tree" && !skillHex40.MatchString(h) || kind == "content" && !skillHex64.MatchString(h)) {
		return nil, false
	}
	return &skillSource{repo: r.Source, ref: r.Ref, entry: r.SkillPath, baseline: h, hashKind: kind}, true
}

// Called after Skill discovery. A receipt binds only to its canonical installation
// and aliases, or to a same-name copy whose complete content matches the baseline.
func (s *Scanner) skillSources() {
	defer func() { s.ownSkillSources(); s.skillManagerSources(); s.skillLocalVersions() }()
	type receiptFile struct {
		path, scope, kind string
		version           int
	}
	files := []receiptFile{{filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), s.Config.Home, "tree", 3}}
	if xdg := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(xdg) && within(xdg, s.Config.Home) {
		files = append(files, receiptFile{filepath.Join(xdg, "skills/.skill-lock.json"), s.Config.Home, "tree", 3})
	}
	for _, project := range s.Config.Projects {
		if p, err := filepath.Abs(project); err == nil {
			files = append(files, receiptFile{filepath.Join(p, "skills-lock.json"), p, "content", 1})
		}
	}
	for _, file := range files {
		b, err := readSmall(file.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			s.diag("skills", file.path, "skill_receipt_unreadable")
			continue
		}
		var lock struct {
			Version int                        `json:"version"`
			Skills  map[string]json.RawMessage `json:"skills"`
		}
		if json.Unmarshal(b, &lock) != nil || lock.Version != file.version || lock.Skills == nil {
			s.diag("skills", file.path, "skill_receipt_schema_unsupported")
			continue
		}
		keys := make([]string, 0, len(lock.Skills))
		for key := range lock.Skills {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, name := range keys {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
				continue
			}
			// Pin evidence must survive malformed typed identity fields.
			var pin struct {
				PinnedRef json.RawMessage `json:"pinnedRef"`
			}
			if json.Unmarshal(lock.Skills[name], &pin) != nil {
				s.diag("skills", file.path, "skill_receipt_schema_unsupported")
				continue
			}
			pinned := unsupportedSkillPin(skillReceipt{PinnedRef: pin.PinnedRef})
			var record skillReceipt
			decodeErr := json.Unmarshal(lock.Skills[name], &record)
			// Parse identity only to bind the manual result; this cannot grant a source.
			identity := record
			identity.PinnedRef = nil
			source, ok := parsedSkillSource(identity, file.kind)
			if decodeErr != nil {
				s.diag("skills", file.path, "skill_receipt_schema_unsupported")
				ok = false
			}
			if !ok && !pinned {
				continue
			}
			canonical := filepath.Join(file.scope, ".agents/skills", name, "SKILL.md")
			canonicalReal := real(canonical)
			for n := range s.Report.Items {
				i := &s.Report.Items[n]
				if i.Kind != "skill" || i.Owner == "system" || i.Source == "plugin-cache" || i.Source == "git" || strings.Contains(i.Path, "/plugins/cache/") {
					continue
				}
				rp := real(i.Path)
				if rp == "" {
					continue
				}
				exact := canonicalReal != "" && rp == canonicalReal
				// A pin protects shared content through every canonical alias.
				if filepath.Base(filepath.Dir(i.Path)) != name && !(pinned && exact) {
					continue
				}
				if !exact {
					if !ok {
						continue
					}
					rel, err := filepath.Rel(file.scope, i.Path)
					if err != nil || strings.HasPrefix(rel, "..") {
						continue
					}
					roots := []string{".agents/skills", ".codex/skills", ".claude/skills", ".config/opencode/skills", ".opencode/skills", ".hermes/skills", ".openclaw/skills"}
					known := false
					for _, root := range roots {
						if filepath.Clean(i.Path) == filepath.Join(file.scope, root, name, "SKILL.md") {
							known = true
						}
					}
					if !known {
						continue
					}
					snapshot, err := readSkillSnapshot(filepath.Dir(rp))
					if err != nil || !snapshot.matchesBaseline(source) {
						continue
					}
				}
				if i.Update.Code == "skill_source_conflict" || i.Update.Code == "skill_source_unverified" {
					continue
				}
				if pinned {
					i.skillSource = nil
					i.Update = result("unknown", "skill_source_unverified")
					i.Evidence = append(i.Evidence, "skill-pin:unsupported", "skill-receipt:"+file.path)
					continue
				}
				candidate := *source
				candidate.root = filepath.Dir(rp)
				candidate.receiptPath, candidate.receiptName, candidate.receiptKind = file.path, name, file.kind
				candidate.receiptDigest = sourceRecordDigest(record, file.kind)
				if i.skillSource != nil {
					previous := *i.skillSource
					if previous != candidate {
						i.skillSource = nil
						i.Update = result("unknown", "skill_source_conflict")
						i.Evidence = append(i.Evidence, "skill-source-conflict")
						continue
					}
					continue
				}
				i.skillSource = &candidate
				i.Evidence = append(i.Evidence, "skill-source:github:"+source.repo, "skill-path:"+source.entry, "skill-manager:skills", "skill-receipt:"+file.path)
				if source.ref != "" {
					i.Evidence = append(i.Evidence, "skill-ref:"+source.ref)
				}
				i.Update = result("unknown", "online_query_required")
			}
		}
	}
}

func (s *Scanner) skillLocalVersions() {
	versions := map[string]string{}
	for n := range s.Report.Items {
		i := &s.Report.Items[n]
		if i.skillSource == nil {
			continue
		}
		root := i.skillSource.root
		if _, exists := versions[root]; !exists {
			versions[root] = ""
			if snapshot, err := readSkillSnapshot(root); err == nil {
				versions[root] = snapshot.tree[:12]
			}
		}
		i.Version = versions[root]
	}
}

type skillFile struct {
	name, mode, hash string
	data             []byte
}
type skillSnapshot struct {
	tree     string
	files    []skillFile
	unhashed bool
}

func skillObject(kind string, data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d%c", kind, len(data), 0)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func readSkillSnapshot(root string) (skillSnapshot, error) {
	snapshot := skillSnapshot{}
	total, count := int64(0), 0
	var walk func(string, string, int) (string, error)
	walk = func(dir, prefix string, depth int) (string, error) {
		if depth > 14 {
			return "", errors.New("skill hash depth limit")
		}
		es, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		type treeEntry struct {
			name, mode, hash string
			dir              bool
		}
		var entries []treeEntry
		for _, e := range es {
			if e.Name() == ".git" {
				snapshot.unhashed = true
				continue
			}
			if e.Name() == "node_modules" {
				snapshot.unhashed = true
			}
			count++
			if count > 4096 {
				return "", errors.New("skill hash entry limit")
			}
			p := filepath.Join(dir, e.Name())
			name := prefix + e.Name()
			mode := "100644"
			hash := ""
			st, err := os.Lstat(p)
			if err != nil {
				return "", err
			}
			switch {
			case st.IsDir():
				hash, err = walk(p, name+"/", depth+1)
				mode = "40000"
			case st.Mode()&os.ModeSymlink != 0:
				snapshot.unhashed = true
				var target string
				target, err = os.Readlink(p)
				if err == nil {
					hash = skillObject("blob", []byte(target))
				}
				mode = "120000"
			case st.Mode().IsRegular():
				if st.Size() > metadataLimit || total+st.Size() > metadataLimit {
					return "", errors.New("skill hash byte limit")
				}
				f, err := openRegular(p)
				if err != nil {
					return "", err
				}
				data, readErr := io.ReadAll(io.LimitReader(f, metadataLimit-total+1))
				f.Close()
				if readErr != nil || int64(len(data))+total > metadataLimit {
					return "", errors.New("skill file changed or exceeds limit")
				}
				total += int64(len(data))
				hash = skillObject("blob", data)
				if st.Mode().Perm()&0111 != 0 {
					mode = "100755"
				}
				snapshot.files = append(snapshot.files, skillFile{name, mode, hash, data})
			default:
				return "", errors.New("unsupported skill file type")
			}
			if err != nil {
				return "", err
			}
			entries = append(entries, treeEntry{e.Name(), mode, hash, st.IsDir()})
		}
		sort.Slice(entries, func(a, b int) bool {
			an, bn := entries[a].name, entries[b].name
			if entries[a].dir {
				an += "/"
			}
			if entries[b].dir {
				bn += "/"
			}
			return an < bn
		})
		var tree []byte
		for _, e := range entries {
			raw, _ := hex.DecodeString(e.hash)
			tree = append(tree, []byte(e.mode+" "+e.name+"\x00")...)
			tree = append(tree, raw...)
		}
		return skillObject("tree", tree), nil
	}
	var err error
	snapshot.tree, err = walk(root, "", 0)
	return snapshot, err
}
func (s skillSnapshot) matchesBaseline(source *skillSource) bool {
	if source.hashKind == "tree" {
		return s.tree == source.baseline
	}
	if s.unhashed {
		return false
	}
	// Hash equality proves the baseline even when an older installer's ordering
	// differs. Unreproduced hashes stay unknown; no approximate hash is accepted.
	for _, fold := range []bool{false, true} {
		files := append([]skillFile(nil), s.files...)
		sort.Slice(files, func(a, b int) bool {
			an, bn := files[a].name, files[b].name
			if fold {
				an, bn = strings.ToLower(an), strings.ToLower(bn)
			}
			if an == bn {
				return files[a].name > files[b].name
			}
			return an < bn
		})
		h := sha256.New()
		for _, f := range files {
			if strings.Contains("/"+f.name+"/", "/node_modules/") {
				continue
			}
			h.Write([]byte(f.name))
			h.Write(f.data)
		}
		if hex.EncodeToString(h.Sum(nil)) == source.baseline {
			return true
		}
	}
	return false
}

type skillTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
}
type skillTree struct {
	SHA       string           `json:"sha"`
	Truncated bool             `json:"truncated"`
	Tree      []skillTreeEntry `json:"tree"`
}

func (s *Scanner) skillJSON(raw string, out any) string {
	if !publicURL(raw, true) {
		return "feed_requires_unsafe_or_authenticated_request"
	}
	ctx := s.Config.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if s.Config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Config.Timeout)
		defer cancel()
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return "invalid_source"
	}
	req.Header.Set("User-Agent", "KeepUpdated")
	resp, e := s.feedClient.Do(req)
	if e != nil {
		return networkCode(e)
	}
	defer resp.Body.Close()
	if skillAPIRateLimited(resp) {
		return skillRateLimitCode
	}
	if resp.StatusCode == 404 {
		return "not_found"
	}
	if resp.StatusCode != 200 {
		return "http_error"
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
	if e != nil {
		return networkCode(e)
	}
	if len(b) > metadataLimit {
		return "output_limit"
	}
	if json.Unmarshal(b, out) != nil {
		return "invalid_response"
	}
	return ""
}

// Batch repository/ref queries; private file bytes and credentials never leave disk.
func (s *Scanner) checkSkillSources() { s.checkSkillSourcesWithGit(runSkillGit) }
func (s *Scanner) checkSkillSourcesWithGit(runner skillGitRunner) {
	if runner == nil {
		runner = runSkillGit
	}
	groups := map[string][]int{}
	for n := range s.Report.Items {
		i := &s.Report.Items[n]
		if i.skillSource != nil {
			key := i.skillSource.repo + "\x00" + i.skillSource.ref
			groups[key] = append(groups[key], n)
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	progress := s.progress("skill-sources", len(keys))
	defer progress.finish()
	for _, key := range keys {
		if s.Config.Context != nil && s.Config.Context.Err() != nil {
			return
		}
		indices := groups[key]
		origin := s.Report.Items[indices[0]].skillSource
		progress.begin(origin.repo)
		if !s.Config.Online {
			progress.done(true)
			continue
		}
		ref := origin.ref
		if ref == "" {
			ref = "HEAD"
		}
		base := "https://api.github.com/repos/" + origin.repo
		var commit struct {
			SHA    string `json:"sha"`
			Commit struct {
				Tree struct {
					SHA string `json:"sha"`
				} `json:"tree"`
			} `json:"commit"`
		}
		code := s.skillJSON(base+"/commits/"+url.PathEscape(ref), &commit)
		var tree skillTree
		if code == "" {
			if !skillHex40.MatchString(commit.SHA) || !skillHex40.MatchString(commit.Commit.Tree.SHA) || skillHex40.MatchString(ref) && commit.SHA != ref {
				code = "invalid_response"
			} else {
				// Existing public feed policy deliberately permits only bounded metadata URLs;
				// query-free tree traversal below avoids a recursive query parameter.
				code = s.skillJSON(base+"/git/trees/"+commit.Commit.Tree.SHA, &tree)
			}
		}
		cache := map[string]skillTree{}
		if code == "" {
			cache[commit.Commit.Tree.SHA] = tree
		}
		var repository *skillGitRepository
		var gitErr error
		gitTried := false
		fallback := func(i *Item) {
			if !gitTried {
				gitTried = true
				ctx := s.Config.Context
				if ctx == nil {
					ctx = context.Background()
				}
				pin := ""
				if skillHex40.MatchString(commit.SHA) {
					pin = commit.SHA
				}
				timedRunner := runner
				if s.Config.Timeout > 0 {
					timedRunner = func(ctx context.Context, dir string, args []string) ([]byte, error) {
						queryCtx, cancel := context.WithTimeout(ctx, s.Config.Timeout)
						defer cancel()
						b, e := runner(queryCtx, dir, args)
						if errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
							return nil, errors.New("skill_git_timeout")
						}
						return b, e
					}
				}
				repository, gitErr = openSkillGit(ctx, origin.repo, origin.ref, pin, timedRunner)
			}
			if gitErr != nil {
				i.Update = result("error", skillGitErrorCode(gitErr))
				return
			}
			s.checkSkillGitSource(i, repository)
		}
		for _, n := range indices {
			if s.cancelled() {
				break
			}
			i := &s.Report.Items[n]
			if code == skillRateLimitCode || gitTried {
				fallback(i)
				continue
			}
			if code != "" {
				i.Update = result("error", code)
				continue
			}
			s.checkSkillSource(i, base, commit.SHA, commit.Commit.Tree.SHA, cache)
			if i.Update.Code == skillRateLimitCode {
				fallback(i)
			}
		}
		if repository != nil {
			repository.close()
		}
		unresolved := false
		for _, n := range indices {
			status := s.Report.Items[n].Update.Status
			unresolved = unresolved || (status != "current" && status != "available")
		}
		progress.done(unresolved)
	}
}
func (s *Scanner) skillTreeAt(base, sha string, cache map[string]skillTree) (skillTree, string) {
	if tree, ok := cache[sha]; ok {
		if tree.Truncated || tree.SHA != sha {
			return tree, "skill_tree_incomplete"
		}
		return tree, ""
	}
	var tree skillTree
	code := s.skillJSON(base+"/git/trees/"+sha, &tree)
	if code == "" && (tree.Truncated || tree.SHA != sha) {
		code = "skill_tree_incomplete"
	}
	if code == "" {
		cache[sha] = tree
	}
	return tree, code
}
func (s *Scanner) checkSkillSource(i *Item, base, commit, root string, cache map[string]skillTree) {
	source := i.skillSource
	tree, code := s.skillTreeAt(base, root, cache)
	folder := path.Dir(source.entry)
	if folder != "." {
		for _, segment := range strings.Split(folder, "/") {
			if code != "" {
				break
			}
			found := ""
			for _, e := range tree.Tree {
				if e.Path == segment && e.Type == "tree" && e.Mode == "040000" && skillHex40.MatchString(e.SHA) {
					if found != "" {
						code = "invalid_response"
						break
					}
					found = e.SHA
				}
			}
			if code != "" {
				break
			}
			if found == "" {
				code = "skill_upstream_path_missing"
				break
			}
			tree, code = s.skillTreeAt(base, found, cache)
		}
	}
	if code != "" {
		status := "error"
		if code == "skill_upstream_path_missing" {
			status = "unknown"
		}
		i.Update = result(status, code)
		return
	}
	entryFound := false
	for _, e := range tree.Tree {
		if e.Path == "SKILL.md" && e.Type == "blob" && (e.Mode == "100644" || e.Mode == "100755") && skillHex40.MatchString(e.SHA) {
			entryFound = true
		}
	}
	if !entryFound {
		i.Update = result("unknown", "skill_upstream_path_missing")
		return
	}
	snapshot, err := readSkillSnapshot(source.root)
	if err != nil {
		i.Update = result("unknown", "skill_local_hash_unavailable")
		return
	}
	i.Evidence = append(i.Evidence, "skill-upstream-commit:"+commit, "skill-upstream-tree:"+tree.SHA, "skill-local-tree:"+snapshot.tree, "skill-update:confirmed-preview-required")
	i.Version = snapshot.tree[:12]
	if source.baseline != "" {
		i.Evidence = append(i.Evidence, "skill-installed-baseline:"+source.baseline)
	}
	i.Latest = tree.SHA[:12]
	if snapshot.tree == tree.SHA {
		i.Update = result("current", "skill_content_current")
		i.Evidence = append(i.Evidence, "skill-local:matches-upstream")
		return
	}
	clean := snapshot.matchesBaseline(source)
	if clean {
		i.Evidence = append(i.Evidence, "skill-local:matches-receipt")
	} else {
		i.Evidence = append(i.Evidence, "skill-local:modified-or-baseline-unreproduced")
	}
	if source.hashKind == "tree" {
		if source.baseline == "" {
			i.Update = result("unknown", "skill_local_modified_or_baseline_unknown")
			return
		}
		if tree.SHA == source.baseline {
			i.Update = result("unknown", "skill_local_modified")
		} else if clean {
			i.Update = result("available", "skill_update_available")
		} else {
			i.Update = result("unknown", "skill_upstream_changed_local_modified")
		}
		return
	}
	// Project receipts use content SHA-256; remote tree equality is checked via
	// per-file blob hashes without downloading any Skill payload.
	same, code := s.skillFilesMatch(base, tree, "", snapshot.files, cache)
	if code != "" {
		i.Update = result("error", code)
		return
	}
	if same {
		i.Update = result("current", "skill_content_current")
	} else if clean {
		i.Update = result("available", "skill_update_available")
	} else {
		i.Update = result("unknown", "skill_local_modified_or_baseline_unknown")
	}
}
func (s *Scanner) skillFilesMatch(base string, tree skillTree, prefix string, local []skillFile, cache map[string]skillTree) (bool, string) {
	remote := map[string]string{}
	count := 0
	var walk func(skillTree, string, int) string
	walk = func(tree skillTree, prefix string, depth int) string {
		if depth > 14 {
			return "skill_tree_incomplete"
		}
		for _, e := range tree.Tree {
			count++
			if count > 4096 {
				return "skill_tree_incomplete"
			}
			if e.Path == "" || e.Path == "." || e.Path == ".." || strings.ContainsAny(e.Path, "/\\\x00") {
				return "invalid_response"
			}
			if e.Path == ".git" || e.Path == "node_modules" {
				continue
			}
			if !skillHex40.MatchString(e.SHA) {
				return "invalid_response"
			}
			if e.Type == "tree" && e.Mode == "040000" {
				next, code := s.skillTreeAt(base, e.SHA, cache)
				if code != "" {
					return code
				}
				if code = walk(next, prefix+e.Path+"/", depth+1); code != "" {
					return code
				}
			} else if e.Type == "blob" && (e.Mode == "100644" || e.Mode == "100755") {
				remote[prefix+e.Path] = e.SHA
			} else {
				return "skill_tree_unsupported_file"
			}
		}
		return ""
	}
	if code := walk(tree, prefix, 0); code != "" {
		return false, code
	}
	for _, f := range local {
		if strings.Contains("/"+f.name+"/", "/node_modules/") {
			continue
		}
		if remote[f.name] != f.hash {
			return false, ""
		}
		delete(remote, f.name)
	}
	return len(remote) == 0, ""
}
