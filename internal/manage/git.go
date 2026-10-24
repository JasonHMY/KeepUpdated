package manage

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

var gitCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var gitBranch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_-]*$`)

func gitCommand(root string, o Options, args ...string) Command {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never", "-c", "credential.helper=", "-c", "submodule.recurse=false", "-C", root}
	return Command{Path: "/usr/bin/git", Args: append(base, args...), Dir: root, Env: []string{"HOME=" + o.Home, "PATH=/usr/bin:/bin", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}}
}

// ResolveGitTargets asks only existing independent Skill repositories and uses
// ls-remote, not fetch: preview never writes a repository or runs Skill code.
func ResolveGitTargets(ctx context.Context, r audit.Report, s Selection, options Options, runner Runner) (map[string]ResolvedTarget, error) {
	o := normalizeOptions(options)
	if runner == nil {
		runner = ExecRunner{}
	}
	result := map[string]ResolvedTarget{}
	if s.Action != "update" {
		return result, nil
	}
	for _, i := range r.Items {
		if i.Kind != "skill" || i.Source != "git" || !s.All && !contains(s.IDs, i.ID) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		resolutionCurrent(o, i.Name)
		root := filepath.Dir(i.Path)
		if !filepath.IsAbs(i.Path) || filepath.Clean(i.Path) != i.Path || filepath.Base(i.Path) != "SKILL.md" || i.Owner == "system" || i.Plugin != nil || strings.Contains(i.Path, "/plugins/cache/") || i.Owner != filepath.Base(filepath.Dir(filepath.Dir(root))) {
			continue
		}
		remote, branch, configHash, err := gitScope(root, o)
		if err != nil {
			continue
		}
		query := func(args ...string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			b, e := runner.Run(ctx, gitCommand(root, o, args...))
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if e != nil {
				result[i.ID] = queryFailureTarget("git-skill", root, e)
			}
			return strings.TrimSpace(string(b)), e
		}
		head, e := query("rev-parse", "--verify", "HEAD")
		if e != nil || !gitCommit.MatchString(head) {
			continue
		}
		upstream, e := query("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
		if e != nil || !gitCommit.MatchString(upstream) {
			if gitPredicateExit(e, 1) { // No tracking ref, not a failed owner query.
				delete(result, i.ID)
			}
			continue
		}
		status, e := query("status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
		if e != nil || status != "" {
			continue
		}
		if _, e = query("merge-base", "--is-ancestor", head, upstream); e != nil {
			if gitPredicateExit(e, 1) { // HEAD is not an ancestor of the tracking ref.
				delete(result, i.ID)
			}
			continue
		}
		line, e := query("ls-remote", "--exit-code", remote, "refs/heads/"+branch)
		if e != nil {
			if gitPredicateExit(e, 2) { // The remote has no matching branch.
				delete(result, i.ID)
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !gitCommit.MatchString(fields[0]) {
			continue
		}
		result[i.ID] = ResolvedTarget{Version: fields[0], FromVersion: head, Source: hashText(configHash + "\x00" + remote + "\x00" + branch + "\x00" + head + "\x00" + upstream), Provider: "git-skill", ManagerPath: "/usr/bin/git", Scope: root, OriginVerified: true}
	}
	return result, ctx.Err()
}

func gitPredicateExit(err error, code int) bool {
	var failure CommandFailure
	return errors.As(err, &failure) && failure.Code == "owner_query_failed" && failure.exitCode == code
}

func gitSkillPlan(i audit.Item, p PlanItem, o Options, root string) PlanItem {
	p.Reason = "skill_update_requires_verified_upstream"
	if i.Source != "git" {
		return p
	}
	_, _, configHash, err := gitScope(root, o)
	if err != nil {
		p.Reason = "skill_repository_requires_review"
		return p
	}
	t, ok := o.ResolvedTargets[i.ID]
	if ok && t.ResolutionError != "" {
		p.Reason, p.TargetVersion = ResolutionFailureReason(t.ResolutionError), ""
		return p
	}
	if !ok || !t.OriginVerified || t.Provider != "git-skill" || t.ManagerPath != "/usr/bin/git" || t.Scope != root || !gitCommit.MatchString(t.Version) || !gitCommit.MatchString(t.FromVersion) {
		return p
	}
	if !contains(i.Evidence, "git-worktree-clean") {
		p.Reason = "skill_local_modifications_or_git_state_unknown"
		return p
	}
	p.Provider = "git-skill"
	p.FromVersion, p.TargetVersion = t.FromVersion, t.Version
	p.Binding.ManagerPath = "/usr/bin/git"
	p.Binding.Scope = root
	p.Binding.ResolvedTarget = &t
	p.Binding.RealPath, _ = filepath.EvalSymlinks(i.Path)
	p.Binding.ManagerFingerprint, err = fileHash("/usr/bin/git")
	if err != nil {
		p.Reason = "git_unavailable"
		return p
	}
	p.Binding.Fingerprint, err = cleanupHash([]string{root})
	if err != nil {
		p.Reason = "skill_repository_fingerprint_failed"
		return p
	}
	p.Binding.Fingerprint += "/" + configHash
	p.AffectedPaths = []string{root}
	if p.FromVersion == p.TargetVersion {
		p.Status, p.Reason = "skipped", "already_current"
		return p
	}
	p.Status, p.Reason = "ready", "clean_tracking_skill_repository_verified"
	return p
}

// Only a standalone, user-owned repository with a simple public HTTPS origin is
// eligible. Local filter/hooks/includes, submodules and larger source repos are manual.
func gitScope(root string, o Options) (remote, branch, digest string, err error) {
	if err = ownedPath(root, false); err != nil {
		return
	}
	st, e := os.Lstat(root)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		err = errors.New("not an independent repository")
		return
	}
	allowed := false
	for _, sr := range o.SkillRoots {
		if filepath.Dir(root) == sr {
			allowed = true
		}
	}
	if !allowed {
		err = errors.New("repository outside skill roots")
		return
	}
	if st, e = os.Lstat(filepath.Join(root, ".git")); e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		err = errors.New("external git metadata")
		return
	}
	if _, e = os.Lstat(filepath.Join(root, ".gitmodules")); !os.IsNotExist(e) {
		err = errors.New("submodules unsupported")
		return
	}
	if err = checkTree(root); err != nil {
		return
	}
	head, e := readMetadata(filepath.Join(root, ".git/HEAD"))
	if e != nil {
		err = e
		return
	}
	ref := strings.TrimSpace(string(head))
	branch, ok := strings.CutPrefix(ref, "ref: refs/heads/")
	if !ok || !gitBranch.MatchString(branch) || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.HasSuffix(branch, "/") {
		err = errors.New("detached or unsupported branch")
		return
	}
	config, e := readMetadata(filepath.Join(root, ".git/config"))
	if e != nil {
		err = e
		return
	}
	section := ""
	tracking, merge := "", ""
	for _, line := range strings.Split(string(config), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			if section != "[core]" && section != `[remote "origin"]` && section != `[branch "`+branch+`"]` {
				err = errors.New("repository configuration requires review")
				return
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			err = errors.New("unsupported git config")
			return
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch section {
		case "[core]":
			if !contains([]string{"repositoryformatversion", "filemode", "bare", "logallrefupdates", "ignorecase", "precomposeunicode"}, key) {
				err = errors.New("git execution configuration unsupported")
				return
			}
			if key == "bare" && value != "false" {
				err = errors.New("bare repository")
				return
			}
		case `[remote "origin"]`:
			switch key {
			case "url":
				if remote != "" {
					err = errors.New("duplicate origin")
					return
				}
				remote = value
			case "fetch":
				if value != "+refs/heads/*:refs/remotes/origin/*" {
					err = errors.New("custom fetch")
					return
				}
			default:
				err = errors.New("custom remote settings")
				return
			}
		default:
			switch key {
			case "remote":
				tracking = value
			case "merge":
				merge = value
			default:
				err = errors.New("custom branch settings")
				return
			}
		}
	}
	u, e := url.Parse(remote)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !regexp.MustCompile(`^/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?$`).MatchString(u.Path) || tracking != "origin" || merge != "refs/heads/"+branch {
		err = errors.New("unverified tracking source")
		return
	}
	digest = hashText(string(config) + "\x00" + string(head))
	return
}

func applyGit(ctx context.Context, p PlanItem, o Options, runner Runner) error {
	remote, branch, configHash, err := gitScope(p.Binding.Scope, o)
	if err != nil {
		return err
	}
	if !gitCommit.MatchString(p.TargetVersion) || !gitCommit.MatchString(p.FromVersion) {
		return errors.New("invalid approved commits")
	}
	trackingRef := "refs/remotes/origin/" + branch
	tracking, err := runner.Run(ctx, gitCommand(p.Binding.Scope, o, "rev-parse", "--verify", trackingRef))
	oldTracking := strings.TrimSpace(string(tracking))
	if err != nil || !gitCommit.MatchString(oldTracking) || p.Binding.ResolvedTarget == nil || hashText(configHash+"\x00"+remote+"\x00"+branch+"\x00"+p.FromVersion+"\x00"+oldTracking) != p.Binding.ResolvedTarget.Source {
		return errors.New("tracking source changed since preview")
	}
	head, err := runner.Run(ctx, gitCommand(p.Binding.Scope, o, "rev-parse", "--verify", "HEAD"))
	if err != nil || strings.TrimSpace(string(head)) != p.FromVersion {
		return errors.New("local HEAD changed since preview")
	}
	if _, err = runner.Run(ctx, gitCommand(p.Binding.Scope, o, "fetch", "--no-tags", "--no-recurse-submodules", remote, "refs/heads/"+branch)); err != nil {
		return err
	}
	b, err := runner.Run(ctx, gitCommand(p.Binding.Scope, o, "rev-parse", "--verify", "FETCH_HEAD"))
	if err != nil || strings.TrimSpace(string(b)) != p.TargetVersion {
		return errors.New("remote moved since preview")
	}
	if _, err = runner.Run(ctx, gitCommand(p.Binding.Scope, o, "merge-base", "--is-ancestor", p.FromVersion, p.TargetVersion)); err != nil {
		return errors.New("upstream is not fast-forward")
	}
	// Fetch can wait while another client changes this worktree's branch or source.
	currentRemote, currentBranch, currentConfig, err := gitScope(p.Binding.Scope, o)
	if err != nil || currentRemote != remote || currentBranch != branch || currentConfig != configHash {
		return errors.New("repository source or branch changed during fetch")
	}
	head, err = runner.Run(ctx, gitCommand(p.Binding.Scope, o, "rev-parse", "--verify", "HEAD"))
	if err != nil || strings.TrimSpace(string(head)) != p.FromVersion {
		return errors.New("local HEAD changed during fetch")
	}
	if _, err = runner.Run(ctx, gitCommand(p.Binding.Scope, o, "update-ref", "--no-deref", trackingRef, p.TargetVersion, oldTracking)); err != nil {
		return errors.New("tracking reference changed during update")
	}
	_, err = runner.Run(ctx, gitCommand(p.Binding.Scope, o, "merge", "--ff-only", "--no-edit", p.TargetVersion))
	return err
}

func gitVerified(ctx context.Context, p PlanItem, o Options, runner Runner) bool {
	b, err := runner.Run(ctx, gitCommand(p.Binding.Scope, o, "rev-parse", "--verify", "HEAD"))
	if err != nil || strings.TrimSpace(string(b)) != p.TargetVersion {
		return false
	}
	status, err := runner.Run(ctx, gitCommand(p.Binding.Scope, o, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none"))
	if err != nil || strings.TrimSpace(string(status)) != "" {
		return false
	}
	metadata, err := readMetadata(p.Binding.Path)
	if err != nil || !strings.HasPrefix(string(metadata), "---") {
		return false
	}
	return true
}
