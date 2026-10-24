// Package resolve queries installation owners without changing installed software.
package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

type Resolver struct {
	Options       manage.Options
	Timeout       time.Duration
	Runner        manage.Runner
	MCPHTTPClient *http.Client
	queries       *querySession
}

func (r Resolver) Resolve(ctx context.Context, report audit.Report, selection manage.Selection) (map[string]manage.ResolvedTarget, error) {
	out := map[string]manage.ResolvedTarget{}
	if r.Runner == nil {
		r.Runner = manage.ExecRunner{}
	}
	if r.Timeout == 0 {
		r.Timeout = 30 * time.Second
	}
	r.queries = &querySession{entries: map[string]*queryResult{}}
	ownerOptions := r.Options
	type resolutionJob struct {
		current string
		run     func(context.Context) (map[string]manage.ResolvedTarget, error)
	}
	// Plugin refreshes and copied-Skill staging share this serial job. Only
	// independent read queries run beside it; installation ordering is unchanged.
	jobs := []resolutionJob{{"Skill and plugin sources", func(ctx context.Context) (map[string]manage.ResolvedTarget, error) {
		targets := map[string]manage.ResolvedTarget{}
		for _, resolve := range []func(context.Context, audit.Report, manage.Selection, manage.Options, manage.Runner) (map[string]manage.ResolvedTarget, error){manage.ResolveGitTargets, manage.ResolvePluginTargets} {
			resolved, err := resolve(ctx, report, selection, ownerOptions, r.Runner)
			if err != nil {
				return nil, err
			}
			for id, target := range resolved {
				targets[id] = target
			}
		}
		copied, err := manage.ResolveCopiedSkillTargets(ctx, report, selection, ownerOptions)
		for id, target := range copied {
			targets[id] = target
		}
		return targets, err
	}}}
	for _, owner := range []struct {
		name    string
		resolve func(context.Context, audit.Report, manage.Selection, manage.Options, manage.Runner) (map[string]manage.ResolvedTarget, error)
	}{{"Applications", manage.ResolveApplicationTargets}, {"Native command-line tools", manage.ResolveNativeCLITargets}, {"App Store manual handling", manage.ResolveAppStoreTargets}, {"System and package requests", manage.ResolvePrivilegedTargets}} {
		jobs = append(jobs, resolutionJob{owner.name, func(ctx context.Context) (map[string]manage.ResolvedTarget, error) {
			return owner.resolve(ctx, report, selection, ownerOptions, r.Runner)
		}})
	}
	jobs = append(jobs, resolutionJob{"MCP launcher candidates", func(ctx context.Context) (map[string]manage.ResolvedTarget, error) {
		return r.mcpCandidates(ctx, report, selection)
	}})
	resolvedSelection := manage.ResolutionSelection(report, selection)
	selected := map[string]bool{}
	for _, id := range resolvedSelection.IDs {
		selected[id] = true
	}
	for _, item := range report.Items {
		if selected[item.ID] {
			for _, id := range item.Related {
				selected[id] = true
			}
		}
	}
	type packageGroup struct {
		item audit.Item
		ids  []string
	}
	groups := []packageGroup{}
	byInstallation := map[string]int{}
	for _, item := range report.Items {
		if !(selection.All || selected[item.ID]) || selection.Action != "update" && item.Owner != "pnpm" && item.Kind != "python-package" {
			continue
		}
		if item.Kind != "formula" && item.Kind != "cask" && item.Kind != "node-package" && item.Kind != "python-package" {
			continue
		}
		// Only identical logical installations share a query. Canonical paths can
		// belong to different managers/scopes, and ownership evidence must match.
		installation := item
		installation.ID = ""
		encoded, _ := json.Marshal(installation)
		key := string(encoded)
		if index, ok := byInstallation[key]; ok {
			groups[index].ids = append(groups[index].ids, item.ID)
			continue
		}
		byInstallation[key] = len(groups)
		groups = append(groups, packageGroup{item, []string{item.ID}})
	}
	for _, group := range groups {
		jobs = append(jobs, resolutionJob{group.item.Name, func(ctx context.Context) (map[string]manage.ResolvedTarget, error) {
			target, present := r.resolvePackage(ctx, group.item, report, selection)
			targets := map[string]manage.ResolvedTarget{}
			if present {
				for _, id := range group.ids {
					targets[id] = target
				}
			}
			return targets, ctx.Err()
		}})
	}
	total := len(jobs)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := audit.NewPhaseProgress(workCtx, r.Options.Progress, "resolve", &total)
	defer progress.Finish()
	ownerOptions.Progress = func(event audit.Progress) { progress.Begin(event.Current) }
	results := make([]map[string]manage.ResolvedTarget, total)
	queue := make(chan int)
	var wg sync.WaitGroup
	var failure error
	var failOnce sync.Once
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range queue {
				if workCtx.Err() != nil {
					continue
				}
				progress.Begin(jobs[index].current)
				targets, err := jobs[index].run(workCtx)
				if err != nil {
					failOnce.Do(func() { failure = err; cancel() })
					continue
				}
				results[index] = targets
				unresolved := false
				for _, target := range targets {
					unresolved = unresolved || target.ResolutionError != "" || !target.OriginVerified
				}
				progress.Done(unresolved)
			}
		}()
	}
	for index := range jobs {
		select {
		case queue <- index:
		case <-workCtx.Done():
		}
	}
	close(queue)
	wg.Wait()
	if failure != nil {
		return nil, failure
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Merge in the original resolver order so scheduling never changes precedence.
	for _, targets := range results {
		for id, target := range targets {
			out[id] = target
		}
	}
	return out, nil
}

func (r Resolver) resolvePackage(ctx context.Context, item audit.Item, report audit.Report, selection manage.Selection) (manage.ResolvedTarget, bool) {
	binding, err := manage.ResolutionBinding(item, report, r.Options)
	if err != nil {
		return manage.ResolvedTarget{ResolutionError: failureReason(err)}, true
	}
	var target manage.ResolvedTarget
	if selection.Action == "uninstall" && item.Kind == "python-package" {
		var policy *manage.PythonPolicy
		policy, err = r.pythonPolicy(ctx, item, binding, "uninstall")
		if err == nil {
			target = manage.ResolvedTarget{Python: policy, Version: item.Version, Source: "original-manager-scope", OriginVerified: true, Reason: "original_manager_scope_verified"}
		}
	} else if selection.Action == "uninstall" {
		if err = r.nodeOwner(ctx, item, binding); err == nil {
			target = manage.ResolvedTarget{Version: item.Version, Source: "original-manager-scope", OriginVerified: true, Reason: "original_manager_scope_verified"}
		}
	} else {
		switch item.Owner {
		case "homebrew":
			target, err = r.brew(ctx, item, binding)
		case "npm", "pnpm":
			target, err = r.node(ctx, item, binding)
		case "pipx", "uv":
			target, err = r.python(ctx, item, binding)
		case "bun":
			target, err = r.bun(ctx, item, binding)
		default:
			return manage.ResolvedTarget{}, false
		}
	}
	if err != nil {
		return manage.ResolvedTarget{ResolutionError: failureReason(err)}, true
	}
	target.Provider, target.ManagerPath, target.Scope = item.Owner, binding.ManagerPath, binding.Scope
	return target, true
}

func (r Resolver) query(ctx context.Context, owner string, binding manage.Binding, args ...string) ([]byte, error) {
	if owner == "pnpm" && len(args) > 0 && args[0] != "config" && args[0] != "view" {
		args = append(args, manage.PnpmDisableRuntime)
	}
	if owner == "pnpm" && len(args) > 0 && (args[0] == "--version" || args[0] == "root" || args[0] == "list") {
		args = append(args, "--config.manage-package-manager-versions=false", "--config.ignore-pnpmfile=true")
	}
	if len(args) > 0 && (args[0] == "config" || args[0] == "view") {
		switch owner {
		case "npm":
			args = append(args, "--global", "--prefix", binding.Scope)
		case "pnpm":
			args = append(args, manage.PnpmDirectoryArgs(binding, r.Options, binding.NodeVirtualStore)...)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	dir := r.Options.Home
	if owner == "bun" {
		dir = binding.Scope
	}
	command := manage.Command{Path: binding.ManagerPath, Args: args, Dir: dir, Env: manage.ManagerEnvironment(owner, binding, r.Options)}
	if r.queries != nil && len(args) > 0 && (args[0] == "root" || args[0] == "ls" || args[0] == "list" || args[0] == "config" || args[0] == "--version") {
		return r.queries.run(ctx, r.Runner, command)
	}
	return r.Runner.Run(ctx, command)
}

func (r Resolver) nodeOwner(ctx context.Context, item audit.Item, binding manage.Binding) error {
	var rootArgs, listArgs []string
	if item.Owner == "npm" {
		rootArgs = []string{"root", "--global", "--prefix", binding.Scope}
		listArgs = []string{"ls", "--global", "--prefix", binding.Scope, "--depth=0", "--json"}
	} else {
		rootArgs = []string{"root", "--global", "--config.global-dir=" + manage.PnpmGlobalDir(binding.Scope)}
		listArgs = []string{"list", "--global", "--config.global-dir=" + manage.PnpmGlobalDir(binding.Scope), "--depth=0", "--json"}
	}
	rootData, err := r.query(ctx, item.Owner, binding, rootArgs...)
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootData))
	if !filepath.IsAbs(root) {
		return errors.New("manager root invalid")
	}
	installed, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(item.Name)))
	if err != nil || installed != binding.RealPath {
		return errors.New("manager root differs from installation")
	}
	data, queryErr := r.query(ctx, item.Owner, binding, listArgs...)
	var list struct {
		Dependencies map[string]struct{ Version string }
	}
	if item.Owner == "npm" {
		err = json.Unmarshal(data, &list)
	} else {
		var lists []struct {
			Dependencies map[string]struct{ Version string }
		}
		err = json.Unmarshal(data, &lists)
		if len(lists) != 1 {
			err = errors.New("ambiguous manager root")
		} else {
			list.Dependencies = lists[0].Dependencies
		}
	}
	if err != nil || list.Dependencies[item.Name].Version != item.Version {
		if queryErr != nil {
			return queryErr
		}
		return errors.New("installed package is not the manager direct target")
	}
	// npm ls can return nonzero for another broken dependency; the exact direct row remains required.
	_ = queryErr
	return nil
}

func (r Resolver) node(ctx context.Context, item audit.Item, binding manage.Binding) (manage.ResolvedTarget, error) {
	failed := manage.ResolvedTarget{}
	policy, err := r.nodePolicy(ctx, item, binding)
	if err != nil {
		return failed, err
	}
	binding.NodeVirtualStore = policy.VirtualStore
	if policy.Selection.Source == "npm-default-latest" {
		tag, e := r.query(ctx, item.Owner, binding, "config", "get", "tag")
		if e != nil {
			return failed, e
		}
		if strings.TrimSpace(string(tag)) != "latest" {
			return failed, manage.CommandFailure{Code: "node_selector_requires_review"}
		}
	}
	if item.Owner == "pnpm" {
		bin, e := r.query(ctx, item.Owner, binding, "config", "get", "global-bin-dir")
		if e != nil {
			return failed, e
		}
		value := strings.TrimSpace(string(bin))
		if (value != "undefined" && value != "null" && value != "" && value != policy.Bin) || !manage.NodeBinExists(policy.Bin) {
			return failed, manage.CommandFailure{Code: "node_manager_layout_unsupported"}
		}
	}
	if err := r.nodeOwner(ctx, item, binding); err != nil {
		return failed, err
	}
	registryKey := "registry"
	if strings.HasPrefix(item.Name, "@") {
		registryKey = strings.Split(item.Name, "/")[0] + ":registry"
	}
	registry, err := r.query(ctx, item.Owner, binding, "config", "get", registryKey)
	if err != nil {
		return failed, err
	}
	if value := strings.TrimSpace(string(registry)); value == "undefined" || value == "null" || value == "" {
		registry, err = r.query(ctx, item.Owner, binding, "config", "get", "registry")
		if err != nil {
			return failed, err
		}
	}
	registryURL, err := url.Parse(strings.TrimSpace(string(registry)))
	if err != nil || registryURL.Scheme != "https" || registryURL.Host == "" || registryURL.User != nil || registryURL.RawQuery != "" || registryURL.Fragment != "" {
		return failed, errors.New("manager registry unsupported")
	}
	var local struct {
		Name, Version string
		Resolved      string `json:"_resolved"`
	}
	metadata, err := readMetadata(filepath.Join(item.Path, "package.json"))
	if err != nil || len(metadata) > 4<<20 || json.Unmarshal(metadata, &local) != nil || local.Name != item.Name || local.Version != item.Version {
		return failed, errors.New("installed metadata invalid")
	}
	if local.Resolved != "" {
		origin, parseErr := url.Parse(local.Resolved)
		if parseErr != nil || origin.Scheme != registryURL.Scheme || origin.Host != registryURL.Host {
			return failed, errors.New("recorded source differs from current registry")
		}
	}
	candidate, err := r.permittedNodeCandidate(ctx, item, binding, policy)
	if err != nil {
		return failed, err
	}
	configFiles := []string{filepath.Join(r.Options.Home, ".npmrc"), filepath.Join(binding.Scope, "etc/npmrc"), filepath.Join(binding.Scope, ".npmrc"), filepath.Join(binding.Scope, "pnpm-workspace.yaml"), filepath.Join(manage.PnpmGlobalDir(binding.Scope), ".npmrc")}
	if item.Owner == "pnpm" {
		// ManagerEnvironment omits XDG_CONFIG_HOME. pnpm independently loads this
		// macOS rc even in global=false mode, including when it did not exist before.
		configFiles = append(configFiles, filepath.Join(r.Options.Home, "Library/Preferences/pnpm/rc"))
	}
	for _, key := range []string{"userconfig", "globalconfig"} {
		path, err := r.query(ctx, item.Owner, binding, "config", "get", key)
		if err != nil {
			return failed, err
		}
		value := strings.TrimSpace(string(path))
		if filepath.IsAbs(value) {
			configFiles = append(configFiles, value)
		} else if value != "undefined" && value != "null" && value != "" {
			return failed, errors.New("manager configuration unreadable")
		}
	}
	if item.Owner == "pnpm" && pnpmRuntimeSelection(configFiles) {
		return failed, manage.CommandFailure{Code: "node_runtime_selection_requires_review"}
	}
	digest, err := configDigest(string(registry), configFiles)
	if err != nil {
		return failed, err
	}
	target := manage.ResolvedTarget{Version: candidate.Version, Source: "manager-config:" + digest, OriginVerified: true, Reason: "configured_manager_source", Node: policy, HasLifecycleScripts: nodeScripts(candidate)}
	return target, nil
}

func (r Resolver) brew(ctx context.Context, item audit.Item, binding manage.Binding) (manage.ResolvedTarget, error) {
	data, err := r.query(ctx, item.Owner, binding, "info", "--json=v2", "--"+item.Kind, item.Name)
	if err != nil {
		return manage.ResolvedTarget{}, err
	}
	var info struct {
		Formulae []struct {
			Name, Tap string
			Versions  struct{ Stable string }
			Revision  int
			Pinned    bool
			Installed []struct{ Version string }
		}
		Casks []struct {
			Token, Tap, Version string
			Artifacts           []map[string]json.RawMessage
		}
	}
	if err != nil || json.Unmarshal(data, &info) != nil {
		return manage.ResolvedTarget{}, errors.New("brew metadata unavailable")
	}
	target := manage.ResolvedTarget{OriginVerified: true, Source: item.Source, Reason: "original_manager_candidate"}
	if item.Kind == "formula" {
		if len(info.Formulae) != 1 || info.Formulae[0].Name != item.Name || info.Formulae[0].Tap != item.Source {
			return manage.ResolvedTarget{}, errors.New("brew source requires review")
		}
		formula := info.Formulae[0]
		if formula.Pinned {
			return manage.ResolvedTarget{}, errors.New("brew version pinned")
		}
		target.Version = formula.Versions.Stable
		if formula.Revision > 0 {
			target.Version += "_" + strconv.Itoa(formula.Revision)
		}
		found := false
		for _, installed := range formula.Installed {
			found = found || installed.Version == item.Version
		}
		if !found {
			return manage.ResolvedTarget{}, errors.New("brew installation changed")
		}
		if comparison, known := audit.CompareVersions(item.Version, target.Version); !known {
			return manage.ResolvedTarget{}, errors.New("brew version scheme unsupported")
		} else if comparison >= 0 {
			target.Version = item.Version
		}
	} else {
		if len(info.Casks) != 1 || info.Casks[0].Token != item.Name || info.Casks[0].Tap != item.Source {
			return manage.ResolvedTarget{}, errors.New("cask source changed")
		}
		cask := info.Casks[0]
		if len(cask.Artifacts) == 0 {
			return manage.ResolvedTarget{}, errors.New("cask effects unknown")
		}
		for _, artifact := range cask.Artifacts {
			for key := range artifact {
				if key != "app" && key != "zap" {
					return manage.ResolvedTarget{}, errors.New("cask installation effects need review")
				}
			}
		}
		target.Version = cask.Version
		if item.Version != target.Version {
			outdated, err := r.query(ctx, item.Owner, binding, "outdated", "--json=v2", "--cask", item.Name)
			if err != nil {
				return manage.ResolvedTarget{}, err
			}
			var state struct {
				Casks []struct {
					Name, CurrentVersion string
					Current              string `json:"current_version"`
				}
			}
			if err != nil || json.Unmarshal(outdated, &state) != nil {
				return manage.ResolvedTarget{}, errors.New("cask candidate unverified")
			}
			found := false
			for _, c := range state.Casks {
				found = found || c.Name == item.Name && c.Current == target.Version
			}
			if !found {
				target.Version = item.Version
			}
		}
	}
	if target.Version != item.Version {
		dependencies, err := r.query(ctx, item.Owner, binding, "deps", "--include-build", "--include-optional", "--include-implicit", "--full-name", "--"+item.Kind, item.Name)
		if err != nil {
			return manage.ResolvedTarget{}, err
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(dependencies), "\n") {
			name := strings.TrimPrefix(strings.TrimSpace(line), "homebrew/core/")
			if name == "" {
				continue
			}
			if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+@-]*$`).MatchString(name) {
				return manage.ResolvedTarget{}, errors.New("brew dependency source requires review")
			}
			if name != item.Name && !seen[name] {
				target.AffectedPackages = append(target.AffectedPackages, name)
				seen[name] = true
			}
		}
		sort.Strings(target.AffectedPackages)
		for _, dependency := range target.AffectedPackages {
			if manage.ProtectedRuntime(dependency) {
				target.RuntimeDependencies = append(target.RuntimeDependencies, r.runtimeDependency(ctx, dependency, binding))
			}
		}
		digest := sha256.Sum256([]byte(item.Source + "\x00" + strings.Join(target.AffectedPackages, "\x00")))
		target.Source = item.Source + ":" + hex.EncodeToString(digest[:])
	}
	if manage.ReviewableRuntime(item) {
		target.RuntimeDependents, err = r.runtimeDependents(ctx, item, binding)
		if err != nil {
			return manage.ResolvedTarget{}, err
		}
		target.RuntimeScopeVerified = true
	}
	return target, nil
}

func configDigest(label string, paths []string) (string, error) {
	h := sha256.New()
	fmt.Fprintln(h, label)
	for _, path := range paths {
		data, err := readMetadata(path)
		if os.IsNotExist(err) {
			fmt.Fprintln(h, path, "absent")
			continue
		}
		if err != nil || len(data) > 4<<20 {
			return "", errors.New("manager configuration unreadable")
		}
		fmt.Fprintln(h, path)
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readMetadata(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4<<20 {
		return nil, errors.New("metadata is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if len(data) > 4<<20 {
		return nil, errors.New("metadata grew beyond limit")
	}
	return data, err
}
