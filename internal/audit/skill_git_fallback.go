package audit

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const skillRateLimitCode = "github_rate_limited"

func skillAPIRateLimited(resp *http.Response) bool {
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden && strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0"
}

type skillGitRunner func(context.Context, string, []string) ([]byte, error)

func runSkillGit(ctx context.Context, dir string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "PATH=/usr/bin:/bin", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if e := cmd.Start(); e != nil {
		return nil, errors.New("skill_git_unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	overLimit := false
	for {
		select {
		case e := <-done:
			if overLimit || skillGitRepositoryOversized(dir) {
				return nil, errors.New("skill_git_repository_limit")
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, errors.New("skill_git_timeout")
			}
			if ctx.Err() != nil {
				return nil, errors.New("skill_git_cancelled")
			}
			if out.overflow {
				return nil, errors.New("skill_git_output_limit")
			}
			if e != nil {
				return nil, errors.New("skill_git_query_failed")
			}
			return out.Bytes(), nil
		case <-ticker.C:
			if !overLimit && skillGitRepositoryOversized(dir) {
				overLimit = true
				cancel()
			}
		}
	}
}
func skillGitRepositoryOversized(dir string) bool {
	var total int64
	count := 0
	over := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		count++
		if count > 100000 {
			over = true
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			st, e := d.Info()
			if e == nil {
				total += st.Size()
				if total > 64<<20 {
					over = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return over
}

type skillGitRepository struct {
	ctx                          context.Context
	cancel                       context.CancelFunc
	dir, repo, ref, commit, tree string
	run                          skillGitRunner
	trees                        map[string]skillTree
}

func (g *skillGitRepository) command(args ...string) ([]byte, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never", "-c", "submodule.recurse=false", "-c", "fetch.recurseSubmodules=false", "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "-c", "fetch.unpackLimit=1", "-C", g.dir}
	return g.run(g.ctx, g.dir, append(base, args...))
}
func openSkillGit(ctx context.Context, repo, ref, pin string, runner skillGitRunner) (*skillGitRepository, error) {
	if !skillRepo.MatchString(repo) || strings.Contains(repo, "..") || !validSkillRef(ref) || strings.HasPrefix(ref, "-") || pin != "" && !skillHex40.MatchString(pin) {
		return nil, errors.New("skill_git_source_invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	dir, e := os.MkdirTemp("", "keepupdated-skill-git-")
	if e != nil {
		cancel()
		return nil, errors.New("skill_git_unavailable")
	}
	if runner == nil {
		runner = runSkillGit
	}
	g := &skillGitRepository{ctx: ctx, cancel: cancel, dir: dir, repo: repo, ref: ref, run: runner, trees: map[string]skillTree{}}
	failed := func(e error) (*skillGitRepository, error) { g.close(); return nil, e }
	remote := "https://github.com/" + repo + ".git"
	commit := pin
	if commit == "" {
		if skillHex40.MatchString(ref) {
			commit = ref
		} else {
			names := []string{ref}
			if ref == "" || ref == "HEAD" {
				names = []string{"HEAD"}
			} else if strings.HasPrefix(ref, "refs/heads/") {
				names = []string{ref}
			} else if strings.HasPrefix(ref, "refs/tags/") {
				names = []string{ref, ref + "^{}"}
			} else {
				names = []string{"refs/heads/" + ref, "refs/tags/" + ref, "refs/tags/" + ref + "^{}"}
			}
			args := append([]string{"ls-remote", "--exit-code", "--", remote}, names...)
			b, e := g.command(args...)
			if e != nil {
				return failed(e)
			}
			commit, e = skillGitResolvedRef(b, names)
			if e != nil {
				return failed(e)
			}
		}
	}
	if _, e = g.command("init", "--bare", "--template=", g.dir); e != nil {
		return failed(e)
	}
	// A promisor remote makes cat-file retrieve only selected missing blobs. Its
	// subprocesses inherit the same fixed environment and safety configuration.
	config := "[core]\nrepositoryformatversion = 1\nbare = true\n[extensions]\npartialClone = origin\n[remote \"origin\"]\nurl = " + remote + "\npromisor = true\npartialclonefilter = blob:none\n"
	if e = os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0600); e != nil {
		return failed(errors.New("skill_git_unavailable"))
	}
	if _, e = g.command("fetch", "--depth=1", "--filter=blob:none", "--no-tags", "--no-recurse-submodules", "--", "origin", commit); e != nil {
		if e.Error() == "skill_git_query_failed" {
			e = errors.New("skill_git_fetch_failed")
		}
		return failed(e)
	}
	b, e := g.command("rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if e != nil || strings.TrimSpace(string(b)) != commit {
		return failed(errors.New("skill_git_commit_mismatch"))
	}
	raw, e := g.command("cat-file", "commit", commit)
	if e != nil {
		return failed(e)
	}
	if skillObject("commit", raw) != commit {
		return failed(errors.New("skill_git_commit_mismatch"))
	}
	first, _, _ := bytes.Cut(raw, []byte{'\n'})
	tree := strings.TrimPrefix(string(first), "tree ")
	if string(first) != "tree "+tree || !skillHex40.MatchString(tree) {
		return failed(errors.New("skill_git_commit_mismatch"))
	}
	g.commit, g.tree = commit, tree
	return g, nil
}
func skillGitResolvedRef(data []byte, names []string) (string, error) {
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !skillHex40.MatchString(fields[0]) {
			return "", errors.New("skill_git_ref_invalid")
		}
		allowed := false
		for _, n := range names {
			if fields[1] == n {
				allowed = true
			}
		}
		if !allowed || refs[fields[1]] != "" {
			return "", errors.New("skill_git_ref_invalid")
		}
		refs[fields[1]] = fields[0]
	}
	candidates := map[string]bool{}
	for name, sha := range refs {
		if strings.HasSuffix(name, "^{}") {
			continue
		}
		if peeled := refs[name+"^{}"]; peeled != "" {
			sha = peeled
		}
		candidates[sha] = true
	}
	if len(candidates) != 1 {
		return "", errors.New("skill_git_ref_ambiguous")
	}
	for sha := range candidates {
		return sha, nil
	}
	return "", errors.New("skill_git_ref_invalid")
}
func (g *skillGitRepository) close() { g.cancel(); _ = os.RemoveAll(g.dir) }
func (g *skillGitRepository) treeAt(sha string) (skillTree, error) {
	if tree, ok := g.trees[sha]; ok {
		return tree, nil
	}
	var tree skillTree
	if !skillHex40.MatchString(sha) {
		return tree, errors.New("skill_tree_invalid")
	}
	raw, e := g.command("cat-file", "tree", sha)
	if e != nil {
		return tree, e
	}
	if skillObject("tree", raw) != sha {
		return tree, errors.New("skill_tree_hash_mismatch")
	}
	tree.SHA = sha
	seen := map[string]bool{}
	for len(raw) > 0 {
		space := bytes.IndexByte(raw, ' ')
		if space <= 0 {
			return tree, errors.New("skill_tree_invalid")
		}
		mode := string(raw[:space])
		rest := raw[space+1:]
		end := bytes.IndexByte(rest, 0)
		if end <= 0 || len(rest) < end+21 {
			return tree, errors.New("skill_tree_invalid")
		}
		name := string(rest[:end])
		id := hex.EncodeToString(rest[end+1 : end+21])
		raw = rest[end+21:]
		typ := "blob"
		switch mode {
		case "40000":
			typ = "tree"
			mode = "040000"
		case "100644", "100755", "120000":
		case "160000":
			typ = "commit"
		default:
			return tree, errors.New("skill_tree_unsupported_file")
		}
		if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || seen[name] {
			return tree, errors.New("skill_tree_unsupported_file")
		}
		seen[name] = true
		tree.Tree = append(tree.Tree, skillTreeEntry{Path: name, Mode: mode, Type: typ, SHA: id})
		if len(tree.Tree) > 4096 {
			return tree, errors.New("skill_tree_entry_limit")
		}
	}
	g.trees[sha] = tree
	return tree, nil
}
func (g *skillGitRepository) snapshot(src SkillUpdateSource, expectedTree, cacheDir string, payload bool) (SkillUpdateSnapshot, error) {
	out := SkillUpdateSnapshot{Commit: g.commit, Files: []SkillUpdateFile{}}
	if src.Repo != g.repo || src.Ref != g.ref || src.Entry == "" || path.Clean(src.Entry) != src.Entry || path.IsAbs(src.Entry) || strings.HasPrefix(src.Entry, "../") || strings.ContainsAny(src.Entry, "\\\x00\r\n") || path.Base(src.Entry) != "SKILL.md" {
		return out, errors.New("skill_git_source_invalid")
	}
	tree, e := g.treeAt(g.tree)
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
			tree, e = g.treeAt(found)
			if e != nil {
				return out, e
			}
		}
	}
	if expectedTree != "" && expectedTree != tree.SHA {
		return out, errors.New("skill_upstream_tree_changed")
	}
	out.Tree = tree.SHA
	count, total := 0, 0
	var walk func(skillTree, string, int) error
	walk = func(t skillTree, prefix string, depth int) error {
		if depth > 14 {
			return errors.New("skill_tree_depth_limit")
		}
		seen := map[string]bool{}
		for _, x := range t.Tree {
			count++
			if count > 4096 {
				return errors.New("skill_tree_entry_limit")
			}
			if strings.EqualFold(x.Path, ".git") || strings.EqualFold(x.Path, "node_modules") || !utf8.ValidString(x.Path) || strings.ContainsAny(x.Path, "\\\x00\r\n\t") || seen[strings.ToLower(x.Path)] || !((x.Type == "tree" && x.Mode == "040000") || (x.Type == "blob" && (x.Mode == "100644" || x.Mode == "100755"))) {
				return errors.New("skill_tree_unsupported_file")
			}
			seen[strings.ToLower(x.Path)] = true
			name := prefix + x.Path
			if x.Type == "tree" {
				next, e := g.treeAt(x.SHA)
				if e != nil {
					return e
				}
				if e = walk(next, name+"/", depth+1); e != nil {
					return e
				}
				continue
			}
			var data []byte
			if payload {
				var hit bool
				data, hit, e = readSkillObjectCache(cacheDir, "blob-"+x.SHA)
				if e != nil {
					return e
				}
				if !hit {
					data, e = g.command("cat-file", "blob", x.SHA)
					if e != nil {
						return e
					}
				}
				if skillObject("blob", data) != x.SHA {
					return errors.New("skill_blob_hash_mismatch")
				}
				total += len(data)
				if total > metadataLimit {
					return errors.New("skill_download_limit")
				}
				if !hit {
					if e = writeSkillObjectCache(cacheDir, "blob-"+x.SHA, data); e != nil {
						return e
					}
				}
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
	sort.Slice(out.Files, func(a, b int) bool { return out.Files[a].Path < out.Files[b].Path })
	return out, nil
}
func downloadSkillGitFallback(ctx context.Context, src SkillUpdateSource, commit, tree, cacheDir string, runner skillGitRunner) (SkillUpdateSnapshot, error) {
	g, e := openSkillGit(ctx, src.Repo, src.Ref, commit, runner)
	if e != nil {
		return SkillUpdateSnapshot{}, e
	}
	defer g.close()
	return g.snapshot(src, tree, cacheDir, true)
}
func skillGitErrorCode(e error) string {
	if e == nil {
		return ""
	}
	if strings.HasPrefix(e.Error(), "skill_") && !strings.ContainsAny(e.Error(), " /:\n") {
		return e.Error()
	}
	return "skill_git_query_failed"
}

func (s *Scanner) checkSkillGitSource(i *Item, g *skillGitRepository) {
	src, e := SkillSourceForUpdate(*i)
	if e != nil {
		i.Update = result("error", "skill_source_unverified")
		return
	}
	remote, e := g.snapshot(src, "", "", false)
	if e != nil {
		i.Update = result("error", skillGitErrorCode(e))
		return
	}
	local, e := readSkillSnapshot(src.Root)
	if e != nil {
		i.Update = result("unknown", "skill_local_hash_unavailable")
		return
	}
	i.Evidence = append(i.Evidence, "skill-upstream-commit:"+remote.Commit, "skill-upstream-tree:"+remote.Tree, "skill-local-tree:"+local.tree, "skill-update:confirmed-preview-required", "skill-query:public-git-rate-limit-fallback")
	i.Version, i.Latest = local.tree[:12], remote.Tree[:12]
	if src.Baseline != "" {
		i.Evidence = append(i.Evidence, "skill-installed-baseline:"+src.Baseline)
	}
	same := local.tree == remote.Tree
	if !same && src.HashKind == "content" {
		files := map[string]string{}
		for _, f := range remote.Files {
			files[f.Path] = f.SHA
		}
		same = true
		for _, f := range local.files {
			if strings.Contains("/"+f.name+"/", "/node_modules/") {
				continue
			}
			if files[f.name] != f.hash {
				same = false
			}
			delete(files, f.name)
		}
		same = same && len(files) == 0
	}
	if same {
		i.Update = result("current", "skill_content_current")
		i.Evidence = append(i.Evidence, "skill-local:matches-upstream")
		return
	}
	clean := local.matchesBaseline(i.skillSource)
	if clean {
		i.Evidence = append(i.Evidence, "skill-local:matches-receipt")
		i.Update = result("available", "skill_update_available")
		return
	}
	i.Evidence = append(i.Evidence, "skill-local:modified-or-baseline-unreproduced")
	code := "skill_local_modified_or_baseline_unknown"
	if src.HashKind == "tree" {
		if remote.Tree == src.Baseline {
			code = "skill_local_modified"
		} else if src.Baseline != "" {
			code = "skill_upstream_changed_local_modified"
		}
	}
	i.Update = result("unknown", code)
}

// SkillUpdateDownloadSession shares only this sequential resolver batch. Close
// removes every temporary bare repository; mutable refs are fresh next batch.
type SkillUpdateDownloadSession struct {
	runner       skillGitRunner
	repositories map[string]*skillGitRepository
	limited      map[string]bool
}

func NewSkillUpdateDownloadSession() *SkillUpdateDownloadSession {
	return newSkillUpdateDownloadSession(runSkillGit)
}
func newSkillUpdateDownloadSession(runner skillGitRunner) *SkillUpdateDownloadSession {
	return &SkillUpdateDownloadSession{runner: runner, repositories: map[string]*skillGitRepository{}, limited: map[string]bool{}}
}
func (s *SkillUpdateDownloadSession) Close() {
	for _, g := range s.repositories {
		g.close()
	}
	s.repositories = map[string]*skillGitRepository{}
}
func (s *SkillUpdateDownloadSession) gitSnapshot(ctx context.Context, src SkillUpdateSource, pin, tree, cacheDir string) (SkillUpdateSnapshot, error) {
	key := src.Repo + "\x00" + src.Ref
	s.limited[key] = true
	g := s.repositories[key]
	if g != nil && pin != "" && g.commit != pin {
		return SkillUpdateSnapshot{}, errors.New("skill_git_commit_mismatch")
	}
	if g == nil {
		var e error
		g, e = openSkillGit(ctx, src.Repo, src.Ref, pin, s.runner)
		if e != nil {
			return SkillUpdateSnapshot{}, e
		}
		s.repositories[key] = g
	}
	return g.snapshot(src, tree, cacheDir, true)
}
