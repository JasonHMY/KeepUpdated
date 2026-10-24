package manage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type MarketplaceRefreshResult struct {
	StateKnown       bool   `json:"state_known"`
	Attempted        bool   `json:"attempted"`
	CommandSucceeded bool   `json:"command_succeeded"`
	Changed          bool   `json:"changed"`
	BeforeCommit     string `json:"before_commit"`
	AfterCommit      string `json:"after_commit,omitempty"`
	Reason           string `json:"reason"`
}

func pluginNetworkGit(root string, o Options, args ...string) Command {
	c := gitCommand(root, o, args...)
	c.Env = append(c.Env, "GIT_ALLOW_PROTOCOL=https", "GIT_NO_REPLACE_OBJECTS=1")
	return c
}

func resolveRemotePlugin(ctx context.Context, t ResolvedTarget, o Options, runner Runner) ResolvedTarget {
	v := t.PluginUpdate
	fail := func(code string) ResolvedTarget { t.OriginVerified = false; t.Reason = code; return t }
	if v == nil {
		return fail("plugin_owner_unverified")
	}
	if cmp, known := audit.CompareVersions(v.ClientVersion, "2.1.287"); !known || cmp < 0 {
		return fail("plugin_marketplace_refresh_unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	query := func(root string, network bool, args ...string) (string, error) {
		c := pluginGitCommand(root, o, args...)
		if network {
			c = pluginNetworkGit(root, o, args...)
		}
		b, e := runner.Run(ctx, c)
		return strings.TrimSpace(string(b)), e
	}
	remote, e := pluginOriginalSource(o, v.Owner.ID)
	if e != nil || !publicGitRemote(remote) {
		return fail("plugin_source_changed")
	}
	branch := v.Branch
	if !gitBranch.MatchString(branch) {
		return fail("plugin_marketplace_untracked")
	}
	ref := "refs/heads/" + branch
	line, e := query(v.Marketplace, true, "ls-remote", "--exit-code", remote, ref)
	fields := strings.Fields(line)
	if e != nil || len(fields) != 2 || fields[1] != ref || !gitCommit.MatchString(fields[0]) {
		return fail("plugin_remote_query_unavailable")
	}
	targetCommit := fields[0]
	v.RemoteVerified = true
	if targetCommit == v.Commit {
		t.Reason = "plugin_remote_verified_requires_reload"
		return t
	}
	stage, e := os.MkdirTemp("", "keepupdated-plugin-remote-")
	if e != nil {
		return fail("plugin_remote_snapshot_unavailable")
	}
	defer os.RemoveAll(stage)
	stage, e = filepath.EvalSymlinks(stage)
	if e != nil {
		return fail("plugin_remote_snapshot_unavailable")
	}
	if _, e = query(stage, false, "init", "--bare"); e != nil {
		return fail("plugin_remote_snapshot_unavailable")
	}
	// Fetch into a disposable repository only. Do not refresh the installed owner in preview.
	if _, e = query(stage, true, "fetch", "--no-tags", "--no-recurse-submodules", remote, ref); e != nil {
		return fail("plugin_remote_snapshot_unavailable")
	}
	fetched, e := query(stage, false, "rev-parse", "--verify", "FETCH_HEAD")
	if e != nil || fetched != targetCommit {
		return fail("plugin_remote_changed_during_preview")
	}
	if _, e = query(stage, false, "merge-base", "--is-ancestor", v.MarketplaceBeforeCommit, targetCommit); e != nil {
		return fail("plugin_remote_history_unverified")
	}
	catalog, e := runner.Run(ctx, pluginGitCommand(stage, o, "show", targetCommit+":.claude-plugin/marketplace.json"))
	if e != nil {
		return fail("plugin_marketplace_unverified")
	}
	parts := strings.Split(v.Owner.ID, "@")
	next, e := pluginRemoteSnapshot(ctx, runner, stage, o, targetCommit, v.Entry)
	if e != nil {
		return fail("plugin_remote_snapshot_unavailable")
	}
	nextVersion, skills, e := pluginContent(next, parts[0])
	if e != nil {
		return fail("plugin_components_require_review")
	}
	kind := "manifest"
	if nextVersion == "" {
		nextVersion = targetCommit[:12]
		kind = "commit"
	}
	if kind != v.VersionKind {
		return fail("plugin_version_requires_review")
	}
	if kind == "manifest" {
		if cmp, known := audit.CompareVersions(v.Owner.Version, nextVersion); !known || cmp > 0 {
			return fail("plugin_version_requires_review")
		}
	}
	if !pluginRemoteCatalog(catalog, parts[1], parts[0], v.Entry, nextVersion) {
		return fail("plugin_source_changed")
	}
	v.TargetCacheFingerprint = ""
	if nextVersion != v.Owner.Version {
		v.TargetCacheFingerprint, e = pluginTargetCacheFingerprint(filepath.Join(filepath.Dir(v.Owner.Root), nextVersion), next)
		if e != nil {
			return fail("plugin_target_cache_exists")
		}
	}
	v.Commit, v.TargetTree, v.Skills, v.CatalogDigest = targetCommit, next.Tree, skills, hashText(string(catalog))
	v.RefreshMarketplace = true
	t.Version, t.Reason = nextVersion, "plugin_marketplace_refresh_requires_confirmation"
	return t
}

func pluginRemoteSnapshot(ctx context.Context, runner Runner, root string, o Options, commit, entry string) (audit.SkillUpdateSnapshot, error) {
	out := audit.SkillUpdateSnapshot{Commit: commit, Files: []audit.SkillUpdateFile{}}
	b, e := runner.Run(ctx, pluginGitCommand(root, o, "rev-parse", "--verify", commit+":"+entry))
	if e != nil {
		return out, e
	}
	out.Tree = strings.TrimSpace(string(b))
	if !gitCommit.MatchString(out.Tree) {
		return out, errors.New("invalid plugin tree")
	}
	b, e = runner.Run(ctx, pluginGitCommand(root, o, "ls-tree", "-r", "-z", commit+":"+entry))
	if e != nil {
		return out, e
	}
	records := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	if len(records) > 4096 {
		return out, errors.New("plugin tree limit")
	}
	seen := map[string]bool{}
	total := 0
	for _, record := range records {
		header, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		key := strings.ToLower(path)
		if !ok || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || !gitCommit.MatchString(fields[2]) || path == "" || filepath.Clean(path) != path || strings.HasPrefix(path, "../") || filepath.IsAbs(path) || strings.ContainsAny(path, "\\\x00\n\r\t") || seen[key] {
			return out, errors.New("unsupported plugin file")
		}
		seen[key] = true
		data, e := runner.Run(ctx, pluginGitCommand(root, o, "cat-file", "blob", fields[2]))
		if e != nil {
			return out, e
		}
		total += len(data)
		if total > 4<<20 {
			return out, errors.New("plugin bytes limit")
		}
		out.Files = append(out.Files, audit.SkillUpdateFile{Path: path, Mode: fields[0], SHA: fields[2], Data: data})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out, nil
}

func pluginRemoteCatalog(data []byte, market, name, entry, version string) bool {
	var c struct {
		Name    string                       `json:"name"`
		Plugins []map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(data, &c) != nil || c.Name != market {
		return false
	}
	matched := 0
	for _, p := range c.Plugins {
		var n string
		_ = json.Unmarshal(p["name"], &n)
		if n != name {
			continue
		}
		matched++
		var source string
		if json.Unmarshal(p["source"], &source) != nil || source != "./"+entry {
			return false
		}
		for key := range p {
			if !contains([]string{"name", "source", "description", "version", "author", "homepage", "repository", "license", "keywords", "category", "tags", "strict"}, key) {
				return false
			}
		}
		if v, ok := p["version"]; ok {
			var declared string
			if json.Unmarshal(v, &declared) != nil || declared != version {
				return false
			}
		}
		if strict, ok := p["strict"]; ok && string(strict) != "true" {
			return false
		}
	}
	return matched == 1
}

func pluginBeforeMatches(current ResolvedTarget, p PlanItem) bool {
	a, b := current.PluginUpdate, p.PluginUpdate
	if !current.OriginVerified || a == nil || b == nil {
		return false
	}
	return current.ManagerPath == p.Binding.ManagerPath && current.Source == p.Binding.ResolvedTarget.Source && reflect.DeepEqual(a.Owner, b.Owner) && a.Commit == b.MarketplaceBeforeCommit && a.SourceIdentity == b.SourceIdentity && a.Entry == b.Entry && a.CurrentTree == b.CurrentTree && a.Fingerprint == b.Fingerprint && a.ClientVersion == b.ClientVersion && a.VersionKind == b.VersionKind && a.SandboxFingerprint == b.SandboxFingerprint && (b.RefreshMarketplace || a.TargetCacheFingerprint == b.TargetCacheFingerprint)
}

func refreshPluginMarketplace(ctx context.Context, p PlanItem, o Options, runner Runner, backup string, r *ItemResult) bool {
	v := p.PluginUpdate
	effect := &MarketplaceRefreshResult{BeforeCommit: v.MarketplaceBeforeCommit, Reason: "plugin_marketplace_refresh_failed"}
	r.MarketplaceRefresh = effect
	catalog, e := readMetadata(filepath.Join(v.Marketplace, ".claude-plugin/marketplace.json"))
	if e != nil || writeSkillAtomic(filepath.Join(backup, "marketplace-before.json"), catalog) != nil {
		return false
	}
	marker, _ := json.Marshal(v)
	if writeSkillAtomic(filepath.Join(backup, "marketplace-binding.json"), marker) != nil {
		return false
	}
	command := pluginCommand(p.Binding.ManagerPath, o, "plugin", "marketplace", "update", strings.Split(v.Owner.ID, "@")[1], "--json")
	for i, env := range command.Env {
		if strings.HasPrefix(env, "GIT_ALLOW_PROTOCOL=") {
			command.Env[i] = "GIT_ALLOW_PROTOCOL=https"
		}
	}
	beforeMetadata, _ := pluginMetadata(filepath.Join(o.Home, ".claude/plugins/known_marketplaces.json"))
	_, beforeGitErr := os.Lstat(filepath.Join(v.Marketplace, ".git"))
	effect.Attempted = true
	data, runErr := runner.Run(ctx, command)
	var receipt struct{ Command, Outcome, Marketplace string }
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	effect.CommandSucceeded = runErr == nil && len(lines) > 0 && json.Unmarshal([]byte(lines[len(lines)-1]), &receipt) == nil && receipt.Command == "marketplace-update" && receipt.Outcome == "ok" && receipt.Marketplace == strings.Split(v.Owner.ID, "@")[1]
	effect.AfterCommit = pluginObservedCommit(ctx, v.Marketplace, o, runner)
	// A readable commit marker does not establish the complete post-refresh state.
	effect.StateKnown = false
	afterMetadata, metadataErr := pluginMetadata(filepath.Join(o.Home, ".claude/plugins/known_marketplaces.json"))
	afterCatalog, catalogErr := readMetadata(filepath.Join(v.Marketplace, ".claude-plugin/marketplace.json"))
	_, afterGitErr := os.Lstat(filepath.Join(v.Marketplace, ".git"))
	effect.Changed = effect.AfterCommit != "" && effect.AfterCommit != effect.BeforeCommit || (beforeGitErr == nil) != (afterGitErr == nil) || metadataErr == nil && hashText(string(beforeMetadata)) != hashText(string(afterMetadata)) || catalogErr == nil && hashText(string(catalog)) != hashText(string(afterCatalog))
	if !effect.CommandSucceeded || effect.AfterCommit != v.Commit {
		r.Status, r.Reason = "verification_unknown", "plugin_marketplace_refresh_stopped_install"
		effect.Reason = r.Reason
		return false
	}
	verifyOptions := o
	verifyOptions.ManagerPaths = map[string]string{"claude": p.Binding.ManagerPath}
	fresh := resolvePlugin(ctx, audit.Item{Path: p.Binding.Path, Plugin: &v.Owner}, audit.Report{}, verifyOptions, runner)
	f := fresh.PluginUpdate
	managerFingerprint, managerErr := fileHash(p.Binding.ManagerPath)
	if managerErr != nil || managerFingerprint != p.Binding.ManagerFingerprint || !fresh.OriginVerified || f == nil || !reflect.DeepEqual(f.Owner, v.Owner) || f.SourceIdentity != v.SourceIdentity || f.CatalogDigest != v.CatalogDigest || f.Commit != v.Commit || f.Entry != v.Entry || f.TargetTree != v.TargetTree || fresh.Version != p.TargetVersion || !reflect.DeepEqual(f.Skills, v.Skills) || f.ClientVersion != v.ClientVersion || f.SandboxFingerprint != v.SandboxFingerprint || v.TargetCacheFingerprint != "" && f.TargetCacheFingerprint != v.TargetCacheFingerprint || !pluginTargetCacheBinding(v.Owner.Root, p.TargetVersion, f.TargetCacheFingerprint) {
		r.Status, r.Reason = "verification_unknown", "plugin_marketplace_refresh_stopped_install"
		effect.Reason = r.Reason
		return false
	}
	effect.StateKnown = true
	effect.Reason = "plugin_marketplace_approved_commit_verified"
	return true
}

func pluginOriginalSource(o Options, id string) (string, error) {
	b, e := pluginMetadata(filepath.Join(o.Home, ".claude/plugins/known_marketplaces.json"))
	if e != nil {
		return "", e
	}
	var m map[string]pluginMarket
	if json.Unmarshal(b, &m) != nil || !audit.PluginID.MatchString(id) {
		return "", errors.New("plugin source unavailable")
	}
	source := m[strings.Split(id, "@")[1]].Source
	if source.Source != "git" || source.Ref != "" || source.SHA != "" || !publicGitRemote(source.URL) {
		return "", errors.New("plugin source changed")
	}
	return source.URL, nil
}

func pluginObservedCommit(ctx context.Context, root string, o Options, runner Runner) string {
	if b, e := runner.Run(ctx, pluginGitCommand(root, o, "rev-parse", "--verify", "HEAD")); e == nil && gitCommit.MatchString(strings.TrimSpace(string(b))) {
		return strings.TrimSpace(string(b))
	}
	if b, e := pluginMetadata(filepath.Join(root, ".gcs-sha")); e == nil && gitCommit.MatchString(strings.TrimSpace(string(b))) {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// Archive markers are only claims: all cached files must match the public Git
// commit before the marker is accepted as the marketplace version.
func pluginReference(ctx context.Context, root, remote string, o Options, runner Runner) (reference, commit, branch string, config []byte, cleanup func(), reason string) {
	query := func(dir string, network bool, args ...string) (string, error) {
		c := pluginGitCommand(dir, o, args...)
		if network {
			c = pluginNetworkGit(dir, o, args...)
		}
		b, e := runner.Run(ctx, c)
		return strings.TrimSpace(string(b)), e
	}
	st, e := os.Lstat(filepath.Join(root, ".git"))
	if e == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", "", "", nil, nil, "plugin_marketplace_unverified"
		}
		config, e = readMetadata(filepath.Join(root, ".git/config"))
		if e != nil || !pluginGitConfigSafe(string(config)) {
			return "", "", "", nil, nil, "plugin_git_config_requires_review"
		}
		original, e := query(root, false, "config", "--get", "remote.origin.url")
		if e != nil || original != remote {
			return "", "", "", nil, nil, "plugin_source_changed"
		}
		clean, e := query(root, false, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
		if e != nil || clean != "" {
			return "", "", "", nil, nil, "plugin_marketplace_modified"
		}
		commit, e = query(root, false, "rev-parse", "--verify", "HEAD")
		if e != nil || !gitCommit.MatchString(commit) {
			return "", "", "", nil, nil, "plugin_marketplace_unverified"
		}
		branch, e = query(root, false, "symbolic-ref", "--quiet", "--short", "HEAD")
		if e != nil || !gitBranch.MatchString(branch) {
			return "", "", "", nil, nil, "plugin_marketplace_untracked"
		}
		upstream, e := query(root, false, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
		if e != nil || upstream != commit {
			return "", "", "", nil, nil, "plugin_marketplace_untracked"
		}
		return root, commit, branch, config, nil, ""
	}
	if !os.IsNotExist(e) {
		return "", "", "", nil, nil, "plugin_marketplace_unverified"
	}
	marker, e := pluginMetadata(filepath.Join(root, ".gcs-sha"))
	if e != nil || !gitCommit.MatchString(strings.TrimSpace(string(marker))) {
		return "", "", "", nil, nil, "plugin_marketplace_unverified"
	}
	commit = strings.TrimSpace(string(marker))
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// HEAD's symbolic branch comes from the original public source, not the marker.
	head, e := query(o.Home, true, "ls-remote", "--symref", remote, "HEAD")
	if e != nil {
		return "", "", "", nil, nil, "plugin_remote_query_unavailable"
	}
	for _, line := range strings.Split(head, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			branch = strings.TrimPrefix(fields[1], "refs/heads/")
		}
	}
	if !gitBranch.MatchString(branch) {
		return "", "", "", nil, nil, "plugin_marketplace_untracked"
	}
	stage, e := os.MkdirTemp("", "keepupdated-plugin-archive-")
	if e != nil {
		return "", "", "", nil, nil, "plugin_remote_snapshot_unavailable"
	}
	cleanup = func() { os.RemoveAll(stage) }
	reference, e = filepath.EvalSymlinks(stage)
	if e != nil {
		return "", "", "", nil, cleanup, "plugin_remote_snapshot_unavailable"
	}
	if _, e = query(reference, false, "init", "--bare"); e != nil {
		return "", "", "", nil, cleanup, "plugin_remote_snapshot_unavailable"
	}
	if _, e = query(reference, true, "fetch", "--no-tags", "--no-recurse-submodules", remote, "refs/heads/"+branch); e != nil {
		return "", "", "", nil, cleanup, "plugin_remote_snapshot_unavailable"
	}
	if _, e = query(reference, false, "merge-base", "--is-ancestor", commit, "FETCH_HEAD"); e != nil {
		return "", "", "", nil, cleanup, "plugin_remote_history_unverified"
	}
	if !pluginArchiveMatches(ctx, root, reference, commit, o, runner) {
		return "", "", "", nil, cleanup, "plugin_marketplace_modified"
	}
	return reference, commit, branch, []byte("archive:" + commit), cleanup, ""
}

func pluginArchiveMatches(ctx context.Context, root, reference, commit string, o Options, runner Runner) bool {
	b, e := runner.Run(ctx, pluginGitCommand(reference, o, "ls-tree", "-r", "-z", commit))
	if e != nil {
		return false
	}
	expected := map[string][2]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00") {
		head, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(head)
		if !ok || len(fields) != 3 || fields[1] != "blob" || !contains([]string{"100644", "100755", "120000"}, fields[0]) || !gitCommit.MatchString(fields[2]) {
			return false
		}
		expected[path] = [2]string{fields[0], fields[2]}
	}
	if len(expected) > 20000 {
		return false
	}
	var total int64
	e = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if rel == ".gcs-sha" {
			return nil
		}
		want, ok := expected[rel]
		if !ok {
			return errors.New("extra archive file")
		}
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		mode := "100644"
		var data []byte
		if st.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(path)
			if e != nil {
				return e
			}
			data = []byte(target)
			mode = "120000"
		} else if !st.Mode().IsRegular() {
			return errors.New("special archive file")
		} else if st.Mode().Perm()&0111 != 0 {
			mode = "100755"
		}
		total += st.Size()
		if total > 128<<20 || st.Size() > 32<<20 {
			return errors.New("archive limit")
		}
		h := sha1.New()
		if data != nil {
			fmt.Fprintf(h, "blob %d\x00", len(data))
			h.Write(data)
		} else {
			fmt.Fprintf(h, "blob %d\x00", st.Size())
			f, e := os.Open(path)
			if e != nil {
				return e
			}
			_, e = io.Copy(h, io.LimitReader(f, st.Size()+1))
			f.Close()
			if e != nil {
				return e
			}
		}
		if mode != want[0] || hex.EncodeToString(h.Sum(nil)) != want[1] {
			return errors.New("archive modified")
		}
		delete(expected, rel)
		return nil
	})
	// Claude's GitHub archive omits the entire .github directory. It is
	// repository automation, not installed plugin content. A partial directory
	// is still checked, and missing files anywhere else remain a modification.
	if _, statErr := os.Lstat(filepath.Join(root, ".github")); os.IsNotExist(statErr) {
		for path := range expected {
			if strings.HasPrefix(path, ".github/") {
				delete(expected, path)
			}
		}
	}
	return e == nil && len(expected) == 0
}
