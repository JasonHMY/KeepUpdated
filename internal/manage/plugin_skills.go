package manage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type PluginUpdate struct {
	TargetCacheFingerprint  string            `json:"target_cache_fingerprint,omitempty"`
	Branch                  string            `json:"branch"`
	SandboxFingerprint      string            `json:"sandbox_fingerprint"`
	VersionKind             string            `json:"version_kind"`
	ClientVersion           string            `json:"client_version"`
	RemoteVerified          bool              `json:"remote_verified"`
	RefreshMarketplace      bool              `json:"refresh_marketplace"`
	MarketplaceBeforeCommit string            `json:"marketplace_before_commit"`
	SourceIdentity          string            `json:"source_identity"`
	CatalogDigest           string            `json:"catalog_digest"`
	Owner                   audit.PluginOwner `json:"owner"`
	Marketplace             string            `json:"marketplace"`
	Entry                   string            `json:"entry"`
	Commit                  string            `json:"commit"`
	SourceDigest            string            `json:"source_digest"`
	CurrentTree             string            `json:"current_tree"`
	TargetTree              string            `json:"target_tree"`
	Fingerprint             string            `json:"fingerprint"`
	Skills                  []string          `json:"skills"`
	ReloadRequired          bool              `json:"reload_required"`
}

type pluginSource struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Ref    string `json:"ref,omitempty"`
	SHA    string `json:"sha,omitempty"`
}
type pluginMarket struct {
	Source          pluginSource `json:"source"`
	InstallLocation string       `json:"installLocation"`
}

func pluginCommand(manager string, o Options, args ...string) Command {
	return Command{Path: manager, Args: args, Dir: o.Home, Env: []string{
		"HOME=" + o.Home, "CLAUDE_CONFIG_DIR=" + filepath.Join(o.Home, ".claude"), "PATH=/usr/bin:/bin", "LC_ALL=C",
		"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "GIT_ALLOW_PROTOCOL=", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null", "GIT_CONFIG_KEY_1=core.fsmonitor", "GIT_CONFIG_VALUE_1=false",
	}}
}

// Only a committed, local marketplace snapshot is eligible. Remote refresh is
// disabled during the owner command, since it has no exact-version option.
func ResolvePluginTargets(ctx context.Context, report audit.Report, selection Selection, options Options, runner Runner) (map[string]ResolvedTarget, error) {
	o := normalizeOptions(options)
	out := map[string]ResolvedTarget{}
	if selection.Action != "update" {
		return out, nil
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	cache := map[string]ResolvedTarget{}
	for _, i := range report.Items {
		if i.Kind != "skill" || i.Plugin == nil || i.Plugin.Client != "claude" || !selection.All && !contains(selection.IDs, i.ID) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		resolutionCurrent(o, i.Name)
		if !under(i.Path, i.Plugin.Root) {
			out[i.ID] = ResolvedTarget{Provider: "claude-plugin", Reason: "plugin_skill_alias_version_bound"}
			continue
		}
		key := i.Plugin.ID + "\x00" + i.Plugin.Root
		t, ok := cache[key]
		if !ok {
			t = resolvePlugin(ctx, i, report, o, runner)
			if !selection.CachedPlugins && t.OriginVerified {
				t = resolveRemotePlugin(ctx, t, o, runner)
			}
			cache[key] = t
		}
		out[i.ID] = t
	}
	return out, ctx.Err()
}

func resolvePlugin(ctx context.Context, i audit.Item, report audit.Report, o Options, runner Runner) (t ResolvedTarget) {
	t.Provider, t.Reason = "claude-plugin", "plugin_owner_unverified"
	fail := func(reason string) ResolvedTarget { t.Reason = reason; return t }
	a := i.Plugin
	if a == nil || a.Client != "claude" || a.Scope != "user" || !audit.PluginID.MatchString(a.ID) {
		return fail("plugin_scope_requires_client")
	}
	if _, e := pluginMetadata(filepath.Join(o.Home, ".claude/plugins/installed_plugins.json")); e != nil {
		return fail("plugin_installation_unverified")
	}
	records, e := audit.ReadClaudePluginRegistry(o.Home)
	rows := records[a.ID]
	if e != nil || len(rows) != 1 || rows[0].Scope != "user" || rows[0].ProjectPath != "" || rows[0].InstallPath != a.Root || rows[0].Version != a.Version || rows[0].GitCommitSHA != a.Commit {
		return fail("plugin_owner_ambiguous")
	}
	parts := strings.Split(a.ID, "@")
	if !pluginVersionValid(a.Version) || !gitCommit.MatchString(a.Commit) || a.Root != filepath.Join(o.Home, ".claude/plugins/cache", parts[1], parts[0], a.Version) || !pluginSkillWithin(i.Path, a.Root) {
		return fail("plugin_installation_unverified")
	}
	if e = ownedPath(a.Root, false); e != nil {
		return fail("plugin_installation_unverified")
	}
	if rp, e := filepath.EvalSymlinks(a.Root); e != nil || rp != a.Root {
		return fail("plugin_installation_unverified")
	}
	registry := filepath.Join(o.Home, ".claude/plugins/known_marketplaces.json")
	raw, e := pluginMetadata(registry)
	if e != nil {
		return fail("plugin_marketplace_unverified")
	}
	var markets map[string]pluginMarket
	if json.Unmarshal(raw, &markets) != nil {
		return fail("plugin_marketplace_unverified")
	}
	var rawMarkets map[string]struct {
		Source json.RawMessage `json:"source"`
	}
	if json.Unmarshal(raw, &rawMarkets) != nil {
		return fail("plugin_marketplace_unverified")
	}
	var original pluginSource
	decoder := json.NewDecoder(bytes.NewReader(rawMarkets[parts[1]].Source))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&original) != nil {
		return fail("plugin_source_requires_client")
	}
	m, ok := markets[parts[1]]
	if !ok || m.Source.Source != "git" || m.Source.Ref != "" || m.Source.SHA != "" || m.InstallLocation != filepath.Join(o.Home, ".claude/plugins/marketplaces", parts[1]) {
		return fail("plugin_source_requires_client")
	}
	settings, e := pluginMetadata(filepath.Join(o.Home, ".claude/settings.json"))
	if e != nil {
		return fail("plugin_settings_unverified")
	}
	var settingsData struct {
		EnabledPlugins         map[string]bool `json:"enabledPlugins"`
		ExtraKnownMarketplaces map[string]struct {
			Source json.RawMessage `json:"source"`
		} `json:"extraKnownMarketplaces"`
	}
	if json.Unmarshal(settings, &settingsData) != nil || !settingsData.EnabledPlugins[a.ID] {
		return fail("plugin_disabled_requires_client")
	}
	if configured, ok := settingsData.ExtraKnownMarketplaces[parts[1]]; ok {
		var src pluginSource
		decoder := json.NewDecoder(bytes.NewReader(configured.Source))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&src) != nil || src != m.Source {
			return fail("plugin_source_changed")
		}
	}
	// Read the original source without exposing its URL or credential material.
	if !publicGitRemote(m.Source.URL) {
		return fail("plugin_source_requires_client")
	}
	root := m.InstallLocation
	if rp, e := filepath.EvalSymlinks(root); e != nil || rp != root || ownedPath(root, false) != nil {
		return fail("plugin_marketplace_unverified")
	}
	reference, commit, branch, config, cleanup, reason := pluginReference(ctx, root, m.Source.URL, o, runner)
	if cleanup != nil {
		defer cleanup()
	}
	if reason != "" {
		return fail(reason)
	}
	query := func(args ...string) (string, error) {
		b, e := runner.Run(ctx, pluginGitCommand(reference, o, args...))
		return strings.TrimSpace(string(b)), e
	}
	if _, e = query("merge-base", "--is-ancestor", a.Commit, commit); e != nil {
		return fail("plugin_source_history_changed")
	}
	catalogPath := filepath.Join(root, ".claude-plugin/marketplace.json")
	catalog, e := readMetadata(catalogPath)
	if e != nil {
		return fail("plugin_marketplace_unverified")
	}
	var cat struct {
		Name    string                       `json:"name"`
		Plugins []map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(catalog, &cat) != nil || cat.Name != parts[1] {
		return fail("plugin_marketplace_unverified")
	}
	var entry map[string]json.RawMessage
	for _, v := range cat.Plugins {
		var name string
		_ = json.Unmarshal(v["name"], &name)
		if name == parts[0] {
			if entry != nil {
				return fail("plugin_owner_ambiguous")
			}
			entry = v
		}
	}
	if entry == nil {
		return fail("plugin_marketplace_unverified")
	}
	for k := range entry {
		if !contains([]string{"name", "source", "description", "version", "author", "homepage", "repository", "license", "keywords", "category", "tags", "strict"}, k) {
			return fail("plugin_components_require_review")
		}
	}
	var relative string
	if json.Unmarshal(entry["source"], &relative) != nil || !strings.HasPrefix(relative, "./") || strings.ContainsAny(relative, "\\:\x00\r\n") {
		return fail("plugin_source_requires_client")
	}
	rel := strings.TrimPrefix(relative, "./")
	if rel == "" || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "../") || rel == ".." || rel == "." {
		return fail("plugin_source_requires_client")
	}
	candidate := filepath.Join(root, rel)
	if rp, e := filepath.EvalSymlinks(candidate); e != nil || rp != candidate || !under(candidate, root) {
		return fail("plugin_source_requires_client")
	}
	if _, e := skillFullFingerprint(candidate); e != nil {
		return fail("plugin_content_unverified")
	}
	old, e := audit.ReadSkillUpdateSnapshot(a.Root)
	if e != nil {
		return fail("plugin_content_unverified")
	}
	next, e := audit.ReadSkillUpdateSnapshot(candidate)
	if e != nil {
		return fail("plugin_content_unverified")
	}
	oldTree, e := query("rev-parse", "--verify", a.Commit+":"+rel)
	if e != nil || !pluginMatchesGit(ctx, runner, reference, o, a.Commit, rel, old) {
		return fail("plugin_local_modifications")
	}
	nextTree, e := query("rev-parse", "--verify", commit+":"+rel)
	if e != nil || !pluginMatchesGit(ctx, runner, reference, o, commit, rel, next) {
		return fail("plugin_marketplace_modified")
	}
	oldVersion, _, e := pluginContent(old, parts[0])
	oldDerived := oldVersion == ""
	if oldVersion == "" {
		oldVersion = a.Commit[:12]
	}
	if e != nil || oldVersion != a.Version {
		return fail("plugin_components_require_review")
	}
	nextVersion, skills, e := pluginContent(next, parts[0])
	derived := nextVersion == ""
	if nextVersion == "" {
		nextVersion = commit[:12]
	}
	if e != nil {
		return fail("plugin_components_require_review")
	}
	if v, ok := entry["version"]; ok {
		var declared string
		if json.Unmarshal(v, &declared) != nil || declared != nextVersion {
			return fail("plugin_version_ambiguous")
		}
	}
	if v, ok := entry["strict"]; ok && string(v) != "true" {
		return fail("plugin_components_require_review")
	}
	targetCacheFingerprint := ""
	if nextVersion != a.Version {
		targetCacheFingerprint, e = pluginTargetCacheFingerprint(filepath.Join(filepath.Dir(a.Root), nextVersion), next)
		if e != nil {
			return fail("plugin_target_cache_exists")
		}
	}
	if ownedPath("/usr/bin/sandbox-exec", true) != nil {
		return fail("plugin_update_network_isolation_unavailable")
	}
	sandboxFingerprint, e := fileHash("/usr/bin/sandbox-exec")
	if e != nil {
		return fail("plugin_update_network_isolation_unavailable")
	}
	manager := findManager("claude", report, o, "")
	if manager == "" || ownedPath(manager, true) != nil {
		return fail("plugin_client_unavailable")
	}
	// The documented JSON update contract starts at 2.1.268. Use the short
	// version probe: some native builds truncate long --help output in pipes.
	clientVersion, e := runner.Run(ctx, pluginCommand(manager, o, "--version"))
	match := regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+) \(Claude Code\)$`).FindStringSubmatch(strings.TrimSpace(string(clientVersion)))
	if e != nil || match == nil {
		return fail("plugin_client_update_unsupported")
	}
	if cmp, known := audit.CompareVersions(match[1], "2.1.268"); !known || cmp < 0 {
		return fail("plugin_client_update_unsupported")
	}
	fp, e := skillFullFingerprint(a.Root)
	if e != nil {
		return fail("plugin_content_unverified")
	}
	// Bind only this marketplace entry; unrelated plugin updates must not invalidate a batch.
	latestSource, e := pluginMetadata(registry)
	if e != nil || !bytes.Equal(latestSource, raw) {
		return fail("plugin_source_changed")
	}
	latestSettings, e := pluginMetadata(filepath.Join(o.Home, ".claude/settings.json"))
	if e != nil || !bytes.Equal(latestSettings, settings) {
		return fail("plugin_settings_unverified")
	}
	latestRecords, e := audit.ReadClaudePluginRegistry(o.Home)
	if e != nil || !reflect.DeepEqual(latestRecords[a.ID], rows) {
		return fail("plugin_owner_changed")
	}
	sourceJSON, _ := json.Marshal(m)
	sourceDigest := hashText(string(sourceJSON) + "\x00" + string(catalog) + "\x00" + string(config) + "\x00" + string(settings))
	if cmp, known := audit.CompareVersions(a.Version, nextVersion); !derived && (!known || cmp > 0) {
		return fail("plugin_version_requires_review")
	}
	if oldDerived != derived {
		return fail("plugin_version_requires_review")
	}
	versionKind := "manifest"
	if derived {
		versionKind = "commit"
	}
	t.Version, t.FromVersion, t.ManagerPath, t.Scope, t.Source = nextVersion, a.Version, manager, a.Root, sourceDigest
	t.PluginUpdate = &PluginUpdate{TargetCacheFingerprint: targetCacheFingerprint, Branch: branch, SandboxFingerprint: sandboxFingerprint, VersionKind: versionKind, ClientVersion: match[1], MarketplaceBeforeCommit: commit, SourceIdentity: hashText(string(sourceJSON) + "\x00" + string(settings)), CatalogDigest: hashText(string(catalog)), Owner: *a, Marketplace: root, Entry: rel, Commit: commit, SourceDigest: sourceDigest, CurrentTree: oldTree, TargetTree: nextTree, Fingerprint: fp, Skills: skills, ReloadRequired: true}
	t.OriginVerified, t.Reason = true, "plugin_cached_snapshot_requires_reload"
	return t
}

func publicGitRemote(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && regexp.MustCompile(`^/[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*(?:\.git)?$`).MatchString(u.Path)
}

func pluginContent(s audit.SkillUpdateSnapshot, name string) (string, []string, error) {
	var manifest map[string]json.RawMessage
	skills := []string{}
	for _, f := range s.Files {
		if f.Mode != "100644" {
			return "", nil, errors.New("executable plugin content")
		}
		if f.Path == ".claude-plugin/plugin.json" {
			if json.Unmarshal(f.Data, &manifest) != nil {
				return "", nil, errors.New("manifest invalid")
			}
			continue
		}
		if !(strings.HasPrefix(f.Path, "skills/") || !strings.Contains(f.Path, "/")) || !(strings.HasSuffix(f.Path, ".md") || contains([]string{"LICENSE", "LICENSE.txt", "NOTICE"}, filepath.Base(f.Path))) {
			return "", nil, errors.New("plugin components require review")
		}
		if strings.HasPrefix(f.Path, "skills/") && strings.HasSuffix(f.Path, "/SKILL.md") {
			skills = append(skills, f.Path)
		}
	}
	for k := range manifest {
		if !contains([]string{"name", "version", "description", "author", "homepage", "repository", "license", "keywords"}, k) {
			return "", nil, errors.New("manifest components require review")
		}
	}
	var n, v string
	_ = json.Unmarshal(manifest["name"], &n)
	_ = json.Unmarshal(manifest["version"], &v)
	if n != name || v != "" && !version.MatchString(v) || len(skills) == 0 {
		return "", nil, errors.New("manifest identity or skills missing")
	}
	sort.Strings(skills)
	return v, skills, nil
}

func pluginSkillPlan(i audit.Item, p PlanItem, o Options, s Selection) PlanItem {
	p.Reason = "plugin_owner_unverified"
	if s.Action != "update" {
		p.Reason = "plugin_uninstall_requires_client"
		return p
	}
	if strings.Contains(i.Path, "/.codex/plugins/cache/") || strings.Contains(i.RealPath, "/.codex/plugins/cache/") {
		p.Reason = "codex_plugin_update_in_client"
		return p
	}
	if i.Plugin != nil && !under(i.Path, i.Plugin.Root) {
		p.Reason = "plugin_skill_alias_version_bound"
		return p
	}
	t, ok := o.ResolvedTargets[i.ID]
	if !ok || t.Provider != "claude-plugin" {
		return p
	}
	p.Provider = t.Provider
	if t.Reason != "" {
		p.Reason = t.Reason
	}
	v := t.PluginUpdate
	if !t.OriginVerified || v == nil {
		return p
	}
	if i.Plugin == nil || !reflect.DeepEqual(*i.Plugin, v.Owner) || v.Owner.Root != t.Scope || !pluginSkillWithin(i.Path, t.Scope) {
		p.Status, p.Reason = "blocked", "plugin_owner_changed"
		return p
	}
	p.PluginUpdate = v
	p.Name = v.Owner.ID
	p.FromVersion, p.TargetVersion = t.FromVersion, t.Version
	p.Binding.Identity = v.Owner.ID
	p.Binding.RealPath, p.Binding.Scope = t.Scope, t.Scope
	p.Binding.ManagerPath = t.ManagerPath
	p.Binding.Fingerprint = v.Fingerprint
	var e error
	p.Binding.ManagerFingerprint, e = fileHash(t.ManagerPath)
	if e != nil {
		p.Status, p.Reason = "blocked", "plugin_client_unavailable"
		return p
	}
	t.PluginUpdate = nil
	p.Binding.ResolvedTarget = &t
	p.AffectedPaths = []string{v.Owner.Root, filepath.Join(o.Home, ".claude/plugins/cache", strings.Split(v.Owner.ID, "@")[1], strings.Split(v.Owner.ID, "@")[0], t.Version)}
	if v.RefreshMarketplace {
		p.AffectedPaths = append(p.AffectedPaths, v.Marketplace)
	}
	if p.FromVersion == p.TargetVersion {
		p.Status, p.Reason = "skipped", "already_current"
		if v.CurrentTree != v.TargetTree {
			p.Status, p.Reason = "manual", "plugin_version_unchanged_content_differs"
		}
		return p
	}
	p.Status = "ready"
	return p
}

type nativePlugin struct {
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	Scope          string   `json:"scope"`
	Enabled        bool     `json:"enabled"`
	InstallPath    string   `json:"installPath"`
	ReadFromFolder string   `json:"readFromFolder"`
	Errors         []string `json:"errors"`
}

func applyPlugin(ctx context.Context, p PlanItem, o Options, state string, runner Runner) ItemResult {
	r := ItemResult{ID: p.ID, Name: p.Name, BeforeVersion: p.FromVersion, Status: "blocked", Reason: "plugin_backup_failed"}
	v := p.PluginUpdate
	if v == nil {
		return r
	}
	old, e := audit.ReadSkillUpdateSnapshot(v.Owner.Root)
	currentFingerprint, fe := skillFullFingerprint(v.Owner.Root)
	if e != nil || fe != nil || currentFingerprint != v.Fingerprint {
		r.Reason = "plugin_local_modifications"
		return r
	}
	// Preserve evidence without ever editing the client's registry to roll back.
	backups := filepath.Join(state, "plugin-backups")
	if privateSkillDir(backups) != nil {
		return r
	}
	backup, e := os.MkdirTemp(backups, "plugin-")
	if e != nil {
		return r
	}
	r.BackupPath = backup
	if e = writeSkillFiles(filepath.Join(backup, "plugin"), old); e != nil {
		return r
	}
	beforeRegistry, e := readMetadata(filepath.Join(o.Home, ".claude/plugins/installed_plugins.json"))
	if e != nil {
		return r
	}
	if e = writeSkillAtomic(filepath.Join(backup, "installed_plugins.json"), beforeRegistry); e != nil {
		return r
	}
	saved, e := audit.ReadSkillUpdateSnapshot(filepath.Join(backup, "plugin"))
	if e != nil || !samePluginFiles(saved, old) {
		return r
	}
	if e = ctx.Err(); e != nil {
		r.Status, r.Reason = "skipped", "cancelled_before_execution"
		return r
	}
	if err := recordRecovery(o, backup, ""); err != nil {
		return r
	}
	// Check again after the backup and immediately before handing control to the owner.
	fp, e := skillFullFingerprint(v.Owner.Root)
	managerHash, me := fileHash(p.Binding.ManagerPath)
	if e != nil || fp != v.Fingerprint || me != nil || managerHash != p.Binding.ManagerFingerprint {
		r.Reason = "plugin_installation_changed"
		return r
	}
	verifyOptions := o
	verifyOptions.ManagerPaths = map[string]string{"claude": p.Binding.ManagerPath}
	current := resolvePlugin(ctx, audit.Item{Path: p.Binding.Path, Plugin: &v.Owner}, audit.Report{}, verifyOptions, runner)
	if !pluginBeforeMatches(current, p) || !pluginTargetCacheBinding(v.Owner.Root, p.TargetVersion, v.TargetCacheFingerprint) {
		r.Reason = "plugin_installation_changed"
		return r
	}
	if v.RefreshMarketplace {
		if !refreshPluginMarketplace(ctx, p, o, runner, backup, &r) {
			return r
		}
	}
	b, runErr := runner.Run(ctx, pluginIsolatedCommand(p.Binding.ManagerPath, o, "plugin", "update", v.Owner.ID, "--scope", "user", "--json"))
	r.Executed = true
	var receipt struct {
		Command    string `json:"command"`
		Outcome    string `json:"outcome"`
		PluginID   string `json:"pluginId"`
		Scope      string `json:"scope"`
		NewVersion string `json:"newVersion"`
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	decoded := len(lines) > 0 && json.Unmarshal([]byte(lines[len(lines)-1]), &receipt) == nil
	r.CommandSucceeded = runErr == nil && decoded && receipt.Command == "update" && receipt.Outcome == "ok" && receipt.PluginID == v.Owner.ID && receipt.Scope == "user" && receipt.NewVersion == p.TargetVersion
	parts := strings.Split(v.Owner.ID, "@")
	expected := filepath.Join(o.Home, ".claude/plugins/cache", parts[1], parts[0], p.TargetVersion)
	targetStateKnown := true
	if _, e := os.Lstat(expected); e == nil {
		if v.TargetCacheFingerprint == "" {
			r.Changed = true
		} else if fingerprint, e := skillFullFingerprint(expected); e != nil {
			targetStateKnown = false
		} else if fingerprint != v.TargetCacheFingerprint {
			r.Changed = true
		}
	} else if os.IsNotExist(e) {
		if v.TargetCacheFingerprint != "" {
			r.Changed = true
		}
	} else {
		targetStateKnown = false
	}
	records, re := audit.ReadClaudePluginRegistry(o.Home)
	rows := records[v.Owner.ID]
	r.Status, r.Reason = "verification_unknown", "plugin_update_not_activated"
	if re != nil || len(rows) != 1 {
		r.Reason = "plugin_recovery_required"
		return r
	}
	after := rows[0]
	r.AfterVersion = after.Version
	r.Changed = r.Changed || after.Version != p.FromVersion || after.InstallPath != v.Owner.Root
	if !r.CommandSucceeded {
		if !r.Changed && targetStateKnown {
			unchanged, ue := audit.ReadSkillUpdateSnapshot(v.Owner.Root)
			if ue == nil && samePluginFiles(unchanged, old) && after.Scope == "user" && after.GitCommitSHA == v.Owner.Commit {
				r.Status, r.Reason = "failed", "plugin_owner_failed_original_preserved"
				return r
			}
			if ue == nil && !samePluginFiles(unchanged, old) {
				r.Changed = true
			}
		}
		r.Reason = "plugin_recovery_required"
		return r
	}
	if after.Scope != "user" || after.ProjectPath != "" || after.Version != p.TargetVersion || after.InstallPath != expected || after.GitCommitSHA != v.Commit {
		return r
	}
	if rp, e := filepath.EvalSymlinks(expected); e != nil || rp != expected {
		return r
	}
	installed, ie := audit.ReadSkillUpdateSnapshot(expected)
	source, sourceErr := pluginOriginalSource(o, v.Owner.ID)
	reference, observedCommit, _, _, cleanup, referenceReason := pluginReference(ctx, v.Marketplace, source, o, runner)
	if cleanup != nil {
		defer cleanup()
	}
	if ie != nil || sourceErr != nil || referenceReason != "" || observedCommit != v.Commit || !pluginMatchesGit(ctx, runner, reference, o, v.Commit, v.Entry, installed) {
		r.Reason = "plugin_content_verification_failed"
		return r
	}
	_, skills, ie := pluginContent(installed, parts[0])
	if ie != nil || !reflect.DeepEqual(skills, v.Skills) {
		r.Reason = "plugin_content_verification_failed"
		return r
	}
	// The native listing confirms the registered load path, not an existing session.
	listing, le := runner.Run(ctx, pluginIsolatedCommand(p.Binding.ManagerPath, o, "plugin", "list", "--json"))
	var plugins []nativePlugin
	if le != nil || json.Unmarshal(listing, &plugins) != nil {
		return r
	}
	matches := 0
	for _, plugin := range plugins {
		if plugin.ID != v.Owner.ID {
			continue
		}
		matches++
		if plugin.Scope != "user" || plugin.Version != p.TargetVersion || plugin.InstallPath != expected || plugin.ReadFromFolder != "" || len(plugin.Errors) > 0 || !plugin.Enabled {
			return r
		}
	}
	if matches != 1 {
		return r
	}
	r.Status, r.Reason, r.ReloadRequired = "updated", "plugin_snapshot_installed_reload_required", true
	return r
}

func pluginGitConfigSafe(text string) bool {
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			if section != "[core]" && section != `[remote "origin"]` && !regexp.MustCompile(`^\[branch "[A-Za-z0-9][A-Za-z0-9/_-]*"\]$`).MatchString(section) {
				return false
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch section {
		case "[core]":
			if !contains([]string{"repositoryformatversion", "filemode", "bare", "logallrefupdates", "ignorecase", "precomposeunicode"}, key) || key == "bare" && value != "false" {
				return false
			}
		case `[remote "origin"]`:
			if key != "url" && (key != "fetch" || value != "+refs/heads/*:refs/remotes/origin/*") {
				return false
			}
		default:
			if key != "remote" && key != "merge" {
				return false
			}
			if key == "remote" && value != "origin" {
				return false
			}
		}
	}
	return section != ""
}

func pluginSkillWithin(path, root string) bool {
	real, e := filepath.EvalSymlinks(path)
	return e == nil && under(real, root) && filepath.Base(real) == "SKILL.md"
}

// Native clients may create empty bookkeeping directories. Compare every file,
// mode and blob against Git; the full filesystem fingerprint still binds them.
func pluginMatchesGit(ctx context.Context, runner Runner, root string, o Options, commit, entry string, s audit.SkillUpdateSnapshot) bool {
	b, e := runner.Run(ctx, pluginGitCommand(root, o, "ls-tree", "-r", "-z", commit+":"+entry))
	if e != nil {
		return false
	}
	records := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	if len(records) != len(s.Files) {
		return false
	}
	files := map[string]audit.SkillUpdateFile{}
	for _, f := range s.Files {
		files[f.Path] = f
	}
	for _, record := range records {
		header, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		f, exists := files[path]
		if !ok || len(fields) != 3 || fields[1] != "blob" || !exists || fields[0] != f.Mode || fields[2] != f.SHA {
			return false
		}
		delete(files, path)
	}
	return len(files) == 0
}
func samePluginFiles(a, b audit.SkillUpdateSnapshot) bool { return reflect.DeepEqual(a.Files, b.Files) }

func pluginGitCommand(root string, o Options, args ...string) Command {
	c := gitCommand(root, o, args...)
	c.Env = append(c.Env, "GIT_ALLOW_PROTOCOL=", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1")
	return c
}

func pluginMetadata(path string) ([]byte, error) {
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || ownedPath(path, false) != nil {
		return nil, errors.New("plugin metadata untrusted")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return nil, errors.New("plugin metadata shared")
	}
	return readMetadata(path)
}

func pluginVersionValid(v string) bool {
	return version.MatchString(v) || regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(v)
}

const pluginNetworkDenyProfile = "(version 1)(allow default)(deny network*)"

func pluginIsolatedCommand(manager string, o Options, args ...string) Command {
	c := pluginCommand(manager, o, args...)
	c.Path = "/usr/bin/sandbox-exec"
	c.Args = append([]string{"-p", pluginNetworkDenyProfile, manager}, args...)
	return c
}

// Native commands may prepare an unregistered version cache while loading the
// refreshed marketplace. Reuse it only when every file matches the approved tree.
func pluginTargetCacheFingerprint(root string, approved audit.SkillUpdateSnapshot) (string, error) {
	if _, e := os.Lstat(root); os.IsNotExist(e) {
		return "", nil
	}
	if rp, e := filepath.EvalSymlinks(root); e != nil || rp != root || ownedPath(root, false) != nil {
		return "", errors.New("target cache untrusted")
	}
	snapshot, e := audit.ReadSkillUpdateSnapshot(root)
	if e != nil || !samePluginFiles(snapshot, approved) {
		return "", errors.New("target cache differs")
	}
	return skillFullFingerprint(root)
}

func pluginTargetCacheBinding(installedRoot, targetVersion, fingerprint string) bool {
	root := filepath.Join(filepath.Dir(installedRoot), targetVersion)
	if fingerprint == "" {
		_, e := os.Lstat(root)
		return os.IsNotExist(e)
	}
	current, e := skillFullFingerprint(root)
	return e == nil && current == fingerprint
}
