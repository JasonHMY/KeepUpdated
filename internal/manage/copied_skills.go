package manage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type SkillFileChange struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	OldSize   int    `json:"old_size"`
	NewSize   int    `json:"new_size"`
	OldMode   string `json:"old_mode,omitempty"`
	NewMode   string `json:"new_mode,omitempty"`
	OldSHA    string `json:"old_sha,omitempty"`
	NewSHA    string `json:"new_sha,omitempty"`
	Binary    bool   `json:"binary"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated"`
}
type CopiedSkillPreview struct {
	Repo              string                  `json:"repo"`
	Ref               string                  `json:"ref"`
	Entry             string                  `json:"entry"`
	Commit            string                  `json:"commit"`
	Tree              string                  `json:"tree"`
	Root              string                  `json:"root"`
	CurrentTree       string                  `json:"current_tree"`
	Baseline          string                  `json:"baseline"`
	HashKind          string                  `json:"hash_kind"`
	SourceDigest      string                  `json:"source_digest"`
	StageKey          string                  `json:"stage_key"`
	DirectoryIdentity string                  `json:"directory_identity"`
	ParentIdentity    string                  `json:"parent_identity"`
	Fingerprint       string                  `json:"fingerprint"`
	Source            audit.SkillUpdateSource `json:"source"`
	FileChanges       []SkillFileChange       `json:"file_changes"`
}

func copiedSkillState(o Options) string {
	return filepath.Join(o.Home, "Library/Application Support/KeepUpdated")
}
func copiedStagePath(o Options, key string) string {
	return filepath.Join(copiedSkillState(o), "skill-stages", key)
}
func copiedTransactionPath(o Options, root string) string {
	return filepath.Join(copiedSkillState(o), "skill-transactions", hashText(root))
}
func privateSkillDir(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return errors.New("skill_state_path_invalid")
	}
	if e := os.MkdirAll(p, 0700); e != nil {
		return e
	}
	rp, e := filepath.EvalSymlinks(p)
	if e != nil || rp != p {
		return errors.New("skill_state_path_untrusted")
	}
	st, e := os.Lstat(p)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("skill_state_not_private")
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Getuid()) {
		return errors.New("skill_state_owner_mismatch")
	}
	return nil
}
func skillDirectoryIdentity(p string) (string, error) {
	st, e := os.Lstat(p)
	if e != nil {
		return "", e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("skill_directory_changed")
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Getuid()) || st.Mode().Perm()&0022 != 0 {
		return "", errors.New("skill_directory_not_owned")
	}
	return fmt.Sprintf("%d:%d:%d:%s", s.Dev, s.Ino, s.Uid, st.Mode()), nil
}
func skillFullFingerprint(root string) (string, error) {
	var count int
	var total int64
	var b strings.Builder
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		count++
		if count > 4096 {
			return errors.New("skill_local_hash_limit")
		}
		if e != nil {
			return e
		}
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		ss, ok := st.Sys().(*syscall.Stat_t)
		if !ok || ss.Uid != uint32(os.Getuid()) || st.Mode()&os.ModeSymlink != 0 || (!st.IsDir() && !st.Mode().IsRegular()) || st.Mode().Perm()&0022 != 0 || ss.Nlink > 1 && !st.IsDir() {
			return errors.New("skill_unsupported_local_files")
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s\x00%s\x00%d:%d:%d\x00%s\x00", rel, st.Mode(), ss.Dev, ss.Ino, ss.Uid, st.ModTime().UTC().String())
		if st.Mode().IsRegular() {
			total += st.Size()
			if st.Size() < 0 || total > 8<<20 {
				return errors.New("skill_local_hash_limit")
			}
			h, e := fileHash(p)
			if e != nil {
				return e
			}
			b.WriteString(h)
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	return hashText(b.String()), nil
}
func copiedSkillScope(i audit.Item, o Options) (string, error) {
	if i.Kind != "skill" || i.Source != "local" || i.Owner == "system" || strings.Contains(i.Path, "/plugins/cache/") || filepath.Base(i.Path) != "SKILL.md" || !filepath.IsAbs(i.Path) || filepath.Clean(i.Path) != i.Path {
		return "", errors.New("skill_entry_outside_user_scope")
	}
	rp, e := filepath.EvalSymlinks(i.Path)
	if e != nil {
		return "", e
	}
	root := filepath.Dir(rp)
	homeReal, e := filepath.EvalSymlinks(o.Home)
	if e != nil || homeReal != o.Home || !under(root, o.Home) {
		return "", errors.New("skill_entry_outside_user_scope")
	}
	allowed := false
	for _, r := range o.SkillRoots {
		rr, e := filepath.EvalSymlinks(r)
		if e == nil && rr == r && under(r, o.Home) && r != o.Home && filepath.Dir(root) == r {
			allowed = true
		}
	}
	if !allowed {
		return "", errors.New("skill_entry_outside_user_scope")
	}
	if _, e = skillDirectoryIdentity(root); e != nil {
		return "", e
	}
	if _, e = skillDirectoryIdentity(filepath.Dir(root)); e != nil {
		return "", e
	}
	return root, nil
}

func skillPreviewKey(p CopiedSkillPreview) string {
	p.StageKey = ""
	b, _ := json.Marshal(p)
	return hashText(string(b))
}

// Resolver prepares bounded, verified data only; replacement requires Apply confirmation.
func ResolveCopiedSkillTargets(ctx context.Context, r audit.Report, s Selection, options Options) (map[string]ResolvedTarget, error) {
	o := normalizeOptions(options)
	out := map[string]ResolvedTarget{}
	if s.Action != "update" {
		return out, nil
	}
	cache := map[string]ResolvedTarget{}
	downloads := audit.NewSkillUpdateDownloadSession()
	defer downloads.Close()
	for _, i := range r.Items {
		if i.Kind != "skill" || i.Source != "local" || !s.All && !contains(s.IDs, i.ID) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		resolutionCurrent(o, i.Name)
		t := ResolvedTarget{Provider: "copied-skill", Reason: "skill_source_unverified"}
		out[i.ID] = t
		root, e := copiedSkillScope(i, o)
		if e != nil {
			t.Reason = "skill_entry_outside_user_scope"
			out[i.ID] = t
			continue
		}
		if previous, ok := cache[root]; ok {
			out[i.ID] = previous
			continue
		}
		src, e := audit.SkillSourceForUpdate(i)
		if e != nil || src.Root != root {
			continue
		}
		t.Scope = root
		t.Source = src.SourceDigest
		block := func(reason string) { t.Reason = reason; out[i.ID] = t; cache[root] = t }
		if _, e = os.Lstat(filepath.Join(copiedTransactionPath(o, root), "transaction.json")); !os.IsNotExist(e) {
			block("skill_recovery_required")
			continue
		}
		if e = audit.VerifySkillUpdateSource(src, o.Home); e != nil {
			block("skill_source_receipt_changed")
			continue
		}
		local, e := audit.ReadSkillUpdateSnapshot(root)
		if e != nil {
			block("skill_local_hash_unavailable")
			continue
		}
		localModified := !audit.SkillSnapshotMatchesSource(local, src)
		objectCache := filepath.Join(copiedSkillState(o), "skill-objects")
		if e = privateSkillDir(objectCache); e != nil {
			block("skill_object_cache_unavailable")
			continue
		}
		target, e := downloads.Download(ctx, src, "", "", o.SkillHTTPClient, objectCache)
		if e != nil {
			block(copiedError(e, "skill_download_failed"))
			continue
		}
		commit, tree := target.Commit, target.Tree
		if local.Tree == tree {
			t.Version, t.FromVersion, t.OriginVerified, t.Reason = tree, local.Tree, true, "already_current"
			out[i.ID] = t
			cache[root] = t
			continue
		}
		changes, e := skillChanges(local, target)
		if e != nil {
			block(copiedError(e, "skill_change_preview_unavailable"))
			continue
		}
		di, e := skillDirectoryIdentity(root)
		if e != nil {
			block("skill_directory_changed")
			continue
		}
		parent, e := skillDirectoryIdentity(filepath.Dir(root))
		if e != nil {
			block("skill_directory_changed")
			continue
		}
		fingerprint, e := skillFullFingerprint(root)
		if e != nil {
			block("skill_unsupported_local_files")
			continue
		}
		preview := &CopiedSkillPreview{Repo: src.Repo, Ref: src.Ref, Entry: src.Entry, Commit: commit, Tree: tree, Root: root, CurrentTree: local.Tree, Baseline: src.Baseline, HashKind: src.HashKind, SourceDigest: src.SourceDigest, DirectoryIdentity: di, ParentIdentity: parent, Fingerprint: fingerprint, Source: src, FileChanges: changes}
		preview.StageKey = skillPreviewKey(*preview)
		if localModified {
			t.Version, t.FromVersion, t.OriginVerified, t.Reason, t.SkillUpdate = tree, local.Tree, true, "skill_local_modifications_present", preview
			out[i.ID] = t
			cache[root] = t
			continue
		}
		if e = stageCopiedSkill(o, preview, target); e != nil {
			block(copiedError(e, "skill_stage_unavailable"))
			continue
		}
		t.Version, t.FromVersion, t.OriginVerified, t.Reason, t.SkillUpdate = tree, local.Tree, true, "skill_preview_ready", preview
		out[i.ID] = t
		cache[root] = t
	}
	return out, nil
}
func copiedError(e error, fallback string) string {
	if strings.HasPrefix(e.Error(), "skill_") && !strings.ContainsAny(e.Error(), " \n:/") {
		return e.Error()
	}
	return fallback
}
func skillChanges(old, new audit.SkillUpdateSnapshot) ([]SkillFileChange, error) {
	a, b := map[string]audit.SkillUpdateFile{}, map[string]audit.SkillUpdateFile{}
	keys := map[string]bool{}
	for _, f := range old.Files {
		a[f.Path] = f
		keys[f.Path] = true
	}
	for _, f := range new.Files {
		b[f.Path] = f
		keys[f.Path] = true
	}
	names := []string{}
	for n := range keys {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []SkillFileChange{}
	total := 0
	for _, n := range names {
		x, xok := a[n]
		y, yok := b[n]
		if xok && yok && x.SHA == y.SHA && x.Mode == y.Mode {
			continue
		}
		kind := "modified"
		if !xok {
			kind = "added"
		} else if !yok {
			kind = "deleted"
		} else if x.SHA == y.SHA {
			kind = "mode_changed"
		}
		c := SkillFileChange{Path: n, Kind: kind, OldSize: len(x.Data), NewSize: len(y.Data), OldMode: x.Mode, NewMode: y.Mode, OldSHA: x.SHA, NewSHA: y.SHA}
		c.Binary = !utf8.Valid(x.Data) || !utf8.Valid(y.Data) || bytes.IndexByte(x.Data, 0) >= 0 || bytes.IndexByte(y.Data, 0) >= 0
		if !c.Binary && kind != "mode_changed" {
			var err error
			c.Diff, err = fullSkillDiff(n, x.Data, y.Data)
			if err != nil {
				return nil, err
			}
		}
		total += len(c.Diff) + len(n) + 512
		if total > 1<<20 || len(out) >= 1000 {
			return nil, errors.New("skill_preview_too_large")
		}
		out = append(out, c)
	}
	return out, nil
}
func fullSkillDiff(name string, old, new []byte) (string, error) {
	dir, err := os.MkdirTemp("", "keepupdated-skill-diff-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	before, after := filepath.Join(dir, "before"), filepath.Join(dir, "after")
	if err = os.WriteFile(before, old, 0600); err != nil {
		return "", err
	}
	if err = os.WriteFile(after, new, 0600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/diff", "-u", "-L", "a/"+name, "-L", "b/"+name, before, after)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	var out cappedBuffer
	cmd.Stdout = &out
	err = cmd.Run()
	var exit *exec.ExitError
	if ctx.Err() != nil || out.overflow || err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return "", errors.New("skill_change_preview_unavailable")
	}
	return out.String(), nil
}
func stageCopiedSkill(o Options, p *CopiedSkillPreview, s audit.SkillUpdateSnapshot) error {
	parent := filepath.Dir(copiedStagePath(o, p.StageKey))
	if e := privateSkillDir(parent); e != nil {
		return e
	}
	dest := copiedStagePath(o, p.StageKey)
	if _, e := os.Lstat(dest); e == nil {
		snap, e := audit.ReadSkillUpdateSnapshot(dest)
		if e == nil && snap.Tree == p.Tree {
			return nil
		}
		return errors.New("skill_stage_changed")
	}
	tmp, e := os.MkdirTemp(parent, ".stage-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if e = writeSkillFiles(tmp, s); e != nil {
		return e
	}
	snap, e := audit.ReadSkillUpdateSnapshot(tmp)
	if e != nil || snap.Tree != p.Tree {
		return errors.New("skill_stage_hash_mismatch")
	}
	return os.Rename(tmp, dest)
}
func writeSkillFiles(root string, s audit.SkillUpdateSnapshot) error {
	for _, f := range s.Files {
		p := filepath.Join(root, filepath.FromSlash(f.Path))
		if !under(p, root) || p == root {
			return errors.New("skill_stage_path_invalid")
		}
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return e
		}
		mode := os.FileMode(0644)
		if f.Mode == "100755" {
			mode = 0755
		}
		fd, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if e != nil {
			return e
		}
		_, e = fd.Write(f.Data)
		if e == nil {
			e = fd.Sync()
		}
		ce := fd.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
	}
	return nil
}

// Git tracks executable bits, not local read/write restrictions. Keep those restrictions on replacement.
func preserveSkillPermissions(stage, original string) error {
	return filepath.WalkDir(stage, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stage, p)
		if err != nil {
			return err
		}
		old, err := os.Lstat(filepath.Join(original, rel))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if old.Mode()&os.ModeSymlink != 0 {
			return errors.New("skill_installation_changed_since_preview")
		}
		if old.IsDir() != d.IsDir() {
			return nil
		}
		mode := old.Mode().Perm()
		if !d.IsDir() {
			current, err := d.Info()
			if err != nil {
				return err
			}
			if current.Mode().Perm()&0111 == 0 {
				mode &^= 0111
			} else if mode&0111 == 0 {
				mode |= (mode & 0444) >> 2
			}
		}
		return os.Chmod(p, mode)
	})
}
func copiedSkillPlan(i audit.Item, p PlanItem, o Options) PlanItem {
	p.Status, p.Reason = "blocked", "skill_source_unverified"
	t, ok := o.ResolvedTargets[i.ID]
	if !ok || t.Provider != "copied-skill" {
		return p
	}
	if t.Reason != "skill_preview_ready" {
		if t.SkillUpdate != nil && t.OriginVerified && t.SkillUpdate.StageKey == skillPreviewKey(*t.SkillUpdate) {
			p.SkillUpdate = t.SkillUpdate
			p.Provider = "copied-skill"
			p.FromVersion, p.TargetVersion = t.FromVersion, t.Version
			p.Binding.Scope = t.Scope
			p.Binding.RealPath = filepath.Join(t.Scope, "SKILL.md")
			p.AffectedPaths = []string{t.Scope}
		}
		p.Reason = t.Reason
		if t.Reason == "already_current" {
			p.Status = "skipped"
		}
		return p
	}
	v := t.SkillUpdate
	if !t.OriginVerified || v == nil || v.StageKey != skillPreviewKey(*v) || v.Root != t.Scope || v.Tree != t.Version || v.CurrentTree != t.FromVersion {
		return p
	}
	root, e := copiedSkillScope(i, o)
	if e != nil || root != v.Root {
		return p
	}
	p.Provider = "copied-skill"
	p.Status, p.Reason = "ready", "skill_changes_require_confirmation"
	p.FromVersion, p.TargetVersion = v.CurrentTree, v.Tree
	p.SkillUpdate = v
	p.Binding.RealPath = filepath.Join(root, "SKILL.md")
	p.Binding.Scope = root
	p.Binding.Fingerprint = v.Fingerprint
	// The file preview is already approval-bound on PlanItem; do not duplicate it in the binding.
	t.SkillUpdate = nil
	p.Binding.ResolvedTarget = &t
	p.AffectedPaths = []string{root}
	return p
}

type copiedSkillTransaction struct {
	Schema         string `json:"schema"`
	Root           string `json:"root"`
	StageKey       string `json:"stage_key"`
	BackupPath     string `json:"backup_path"`
	StagePath      string `json:"stage_path"`
	OldReceiptPath string `json:"old_receipt_path,omitempty"`
	Phase          string `json:"phase"`
	Error          string `json:"error,omitempty"`
}

func writeSkillAtomic(p string, data []byte) error {
	tmp, e := os.CreateTemp(filepath.Dir(p), ".receipt-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if e = tmp.Chmod(0600); e == nil {
		_, e = tmp.Write(data)
	}
	if e == nil {
		e = tmp.Sync()
	}
	ce := tmp.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, p); e != nil {
		return e
	}
	return syncSkillDir(filepath.Dir(p))
}
func syncSkillDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func copiedReceiptUnchanged(p *CopiedSkillPreview, o Options) error {
	b, e := audit.ReadOwnSkillReceipt(o.Home, p.Root)
	if p.Source.ReceiptDigest == "" {
		if os.IsNotExist(e) {
			return nil
		}
		return errors.New("skill_source_receipt_changed")
	}
	if e != nil || hashText(string(b)) != p.Source.ReceiptDigest {
		return errors.New("skill_source_receipt_changed")
	}
	return nil
}
func checkCopiedSkillCurrent(p *CopiedSkillPreview, o Options) error {
	if p == nil || p.StageKey != skillPreviewKey(*p) || p.Source.Root != p.Root || p.Source.Repo != p.Repo || p.Source.Ref != p.Ref || p.Source.Entry != p.Entry || p.Source.Baseline != p.Baseline || p.Source.HashKind != p.HashKind || p.Source.SourceDigest != p.SourceDigest {
		return errors.New("skill_preview_invalid")
	}
	if e := audit.VerifySkillUpdateSource(p.Source, o.Home); e != nil {
		return e
	}
	if e := copiedReceiptUnchanged(p, o); e != nil {
		return e
	}
	di, e := skillDirectoryIdentity(p.Root)
	if e != nil || di != p.DirectoryIdentity {
		return errors.New("skill_directory_changed")
	}
	pi, e := skillDirectoryIdentity(filepath.Dir(p.Root))
	if e != nil || pi != p.ParentIdentity {
		return errors.New("skill_parent_changed")
	}
	fingerprint, e := skillFullFingerprint(p.Root)
	if e != nil || fingerprint != p.Fingerprint {
		return errors.New("skill_installation_changed_since_preview")
	}
	local, e := audit.ReadSkillUpdateSnapshot(p.Root)
	if e != nil || local.Tree != p.CurrentTree || !audit.SkillSnapshotMatchesSource(local, p.Source) {
		return errors.New("skill_local_modifications_present")
	}
	return nil
}
func applyCopiedSkill(ctx context.Context, p PlanItem, o Options, stateDir string) (string, bool, error) {
	return applyCopiedSkillWithOps(ctx, p, o, os.Rename, writeSkillAtomic)
}

// The narrow operation seam lets failure tests exercise rollback without touching real Skills.
func applyCopiedSkillWithOps(ctx context.Context, p PlanItem, o Options, rename func(string, string) error, writeReceipt func(string, []byte) error) (backup string, changed bool, err error) {
	o = normalizeOptions(o)
	v := p.SkillUpdate
	if p.Provider != "copied-skill" || v == nil || p.Binding.Scope != v.Root || p.TargetVersion != v.Tree || p.Binding.Fingerprint != v.Fingerprint {
		return "", false, errors.New("skill_preview_invalid")
	}
	if e := ctx.Err(); e != nil {
		return "", false, e
	}
	transactionDir := copiedTransactionPath(o, v.Root)
	if e := privateSkillDir(transactionDir); e != nil {
		return "", false, e
	}
	unlock, e := lockState(transactionDir)
	if e != nil {
		return "", false, e
	}
	defer unlock()
	manifestPath := filepath.Join(transactionDir, "transaction.json")
	if _, e = os.Lstat(manifestPath); !os.IsNotExist(e) {
		return "", false, errors.New("skill_recovery_required")
	}
	if e = checkCopiedSkillCurrent(v, o); e != nil {
		return "", false, e
	}
	stagePath := copiedStagePath(o, v.StageKey)
	if rp, e := filepath.EvalSymlinks(stagePath); e != nil || rp != stagePath {
		return "", false, errors.New("skill_stage_changed")
	}
	target, e := audit.ReadSkillUpdateSnapshot(stagePath)
	if e != nil || target.Tree != v.Tree {
		return "", false, errors.New("skill_stage_changed")
	}
	local, e := audit.ReadSkillUpdateSnapshot(v.Root)
	if e != nil {
		return "", false, e
	}
	changes, e := skillChanges(local, target)
	if e != nil || !reflect.DeepEqual(changes, v.FileChanges) {
		return "", false, errors.New("skill_stage_changed")
	}
	receiptPath := audit.SkillReceiptPath(o.Home, v.Root)
	oldReceipt, oldReceiptErr := audit.ReadOwnSkillReceipt(o.Home, v.Root)
	if oldReceiptErr != nil && !os.IsNotExist(oldReceiptErr) {
		return "", false, oldReceiptErr
	}
	if e = privateSkillDir(filepath.Dir(receiptPath)); e != nil {
		return "", false, e
	}
	parent := filepath.Dir(v.Root)
	container, e := os.MkdirTemp(parent, ".keepupdated-skill-")
	if e != nil {
		return "", false, e
	}
	stage := filepath.Join(container, "updated")
	backup = filepath.Join(container, "original")
	if e = os.Mkdir(stage, 0700); e != nil {
		os.RemoveAll(container)
		return "", false, e
	}
	if e = writeSkillFiles(stage, target); e != nil {
		os.RemoveAll(container)
		return "", false, e
	}
	if e = preserveSkillPermissions(stage, v.Root); e != nil {
		os.RemoveAll(container)
		return "", false, e
	}
	st, e := os.Stat(v.Root)
	if e != nil {
		os.RemoveAll(container)
		return "", false, errors.New("skill_directory_changed")
	}
	if e = os.Chmod(stage, st.Mode().Perm()); e != nil {
		os.RemoveAll(container)
		return "", false, e
	}
	snap, e := audit.ReadSkillUpdateSnapshot(stage)
	if e != nil || snap.Tree != v.Tree {
		os.RemoveAll(container)
		return "", false, errors.New("skill_stage_hash_mismatch")
	}
	targetIdentity, e := skillDirectoryIdentity(stage)
	if e != nil {
		os.RemoveAll(container)
		return "", false, e
	}
	targetFingerprint, e := skillFullFingerprint(stage)
	if e != nil {
		os.RemoveAll(container)
		return "", false, e
	}
	tx := copiedSkillTransaction{Schema: "keepupdated/skill-transaction/v1", Root: v.Root, StageKey: v.StageKey, BackupPath: backup, StagePath: stage, Phase: "prepared"}
	save := func() error { b, _ := json.MarshalIndent(tx, "", "  "); return writeSkillAtomic(manifestPath, b) }
	if e = save(); e != nil {
		os.RemoveAll(container)
		return "", false, e
	}
	if err := recordRecovery(o, backup, manifestPath); err != nil {
		return backup, false, err
	}
	failed := func(cause error) (string, bool, error) {
		tx.Error = copiedError(cause, "skill_update_failed")
		if tx.Phase == "prepared" {
			os.RemoveAll(container)
			if e := os.Remove(manifestPath); e != nil {
				return backup, false, errors.New("skill_recovery_required")
			}
			return "", false, cause
		}
		recovery := func() (string, bool, error) {
			tx.Phase = "recovery_required"
			_ = save()
			return backup, true, errors.New("skill_recovery_required")
		}
		// Validate the original inode before moving a usable installed directory.
		// External edits to that same original directory must return with it.
		if tx.Phase == "backed_up" || tx.Phase == "installed" {
			originalIdentity, e := skillDirectoryIdentity(backup)
			if e != nil || originalIdentity != v.DirectoryIdentity {
				return recovery()
			}
		}
		if tx.Phase == "installed" {
			di, de := skillDirectoryIdentity(v.Root)
			fp, fe := skillFullFingerprint(v.Root)
			if de != nil || fe != nil || di != targetIdentity || fp != targetFingerprint {
				return recovery()
			}
			if e := rename(v.Root, stage); e != nil {
				return recovery()
			}
		}
		if tx.Phase == "backed_up" || tx.Phase == "installed" {
			if _, e := os.Lstat(v.Root); !os.IsNotExist(e) {
				return recovery()
			}
			if e := rename(backup, v.Root); e != nil {
				// If restoring the original fails, keep our verified new tree
				// usable at the installation path while retaining recovery data.
				if tx.Phase == "installed" {
					if _, rootErr := os.Lstat(v.Root); os.IsNotExist(rootErr) {
						di, de := skillDirectoryIdentity(stage)
						fp, fe := skillFullFingerprint(stage)
						if de == nil && fe == nil && di == targetIdentity && fp == targetFingerprint {
							_ = rename(stage, v.Root)
						}
					}
				}
				return recovery()
			}
		}
		tx.Phase = "rolled_back"
		_ = save()
		history := filepath.Join(transactionDir, "last-rollback.json")
		if e := os.Rename(manifestPath, history); e != nil {
			return backup, false, errors.New("skill_recovery_required")
		}
		os.RemoveAll(container)
		return "", false, cause
	}
	if e = checkCopiedSkillCurrent(v, o); e != nil {
		return failed(e)
	}
	if e = ctx.Err(); e != nil {
		return failed(e)
	}
	if e = rename(v.Root, backup); e != nil {
		return failed(e)
	}
	tx.Phase = "backed_up"
	if e = save(); e != nil {
		return failed(e)
	}
	// Renaming is same-volume; verify the complete former directory before replacement.
	fp, e := skillFullFingerprint(backup)
	if e != nil || fp != v.Fingerprint {
		return failed(errors.New("skill_installation_changed_since_preview"))
	}
	if _, e = os.Lstat(v.Root); !os.IsNotExist(e) {
		return failed(errors.New("skill_directory_changed"))
	}
	parentIdentity, e := skillDirectoryIdentity(parent)
	if e != nil || parentIdentity != v.ParentIdentity {
		return failed(errors.New("skill_parent_changed"))
	}
	if e = audit.VerifySkillUpdateSource(v.Source, o.Home); e != nil {
		return failed(e)
	}
	if e = copiedReceiptUnchanged(v, o); e != nil {
		return failed(e)
	}
	if e = rename(stage, v.Root); e != nil {
		return failed(e)
	}
	tx.Phase = "installed"
	if e = save(); e != nil {
		return failed(e)
	}
	actual, e := audit.ReadSkillUpdateSnapshot(v.Root)
	if e != nil || actual.Tree != v.Tree {
		return failed(errors.New("skill_post_update_verification_failed"))
	}
	if e = copiedReceiptUnchanged(v, o); e != nil {
		return failed(e)
	}
	source := v.Source
	source.Baseline, source.HashKind, source.ReceiptDigest = v.Tree, "tree", ""
	receipt := audit.SkillUpdateReceipt{Schema: audit.SkillUpdateReceiptSchema, Source: source, Commit: v.Commit, Tree: v.Tree}
	data, _ := json.MarshalIndent(receipt, "", "  ")
	if oldReceiptErr == nil {
		tx.OldReceiptPath = filepath.Join(transactionDir, "receipt-before.json")
		if e = save(); e != nil {
			return failed(e)
		}
		if e = writeSkillAtomic(tx.OldReceiptPath, oldReceipt); e != nil {
			return failed(e)
		}
	}
	if e = writeReceipt(receiptPath, data); e != nil {
		current, readErr := audit.ReadOwnSkillReceipt(o.Home, v.Root)
		if readErr == nil && bytes.Equal(current, data) {
			var restoreErr error
			if oldReceiptErr == nil {
				restoreErr = writeSkillAtomic(receiptPath, oldReceipt)
			} else {
				restoreErr = os.Remove(receiptPath)
			}
			if restoreErr != nil {
				tx.Phase = "recovery_required"
				_ = save()
				return backup, true, errors.New("skill_recovery_required")
			}
		} else if (readErr == nil && !bytes.Equal(current, oldReceipt)) || (readErr != nil && !os.IsNotExist(readErr)) {
			tx.Phase = "recovery_required"
			_ = save()
			return backup, true, errors.New("skill_recovery_required")
		}
		return failed(e)
	}
	// Receipt write is the commit point. A later durability error keeps the backup
	// and transaction visible; it must not silently restore an old installation.
	tx.Phase = "committed"
	if e = save(); e != nil {
		return backup, true, errors.New("skill_recovery_required")
	}
	if !verifyCopiedSkill(p, o) {
		return backup, true, errors.New("skill_recovery_required")
	}
	if e = syncSkillDir(parent); e != nil {
		return backup, true, errors.New("skill_recovery_required")
	}
	if e = os.Remove(manifestPath); e != nil {
		return backup, true, errors.New("skill_recovery_required")
	}
	if e = syncSkillDir(transactionDir); e != nil {
		return backup, true, errors.New("skill_recovery_required")
	}
	return backup, true, nil
}
func verifyCopiedSkill(p PlanItem, o Options) bool {
	v := p.SkillUpdate
	if v == nil || v.Tree != p.TargetVersion {
		return false
	}
	snap, e := audit.ReadSkillUpdateSnapshot(v.Root)
	if e != nil || snap.Tree != v.Tree {
		return false
	}
	b, e := audit.ReadOwnSkillReceipt(o.Home, v.Root)
	if e != nil {
		return false
	}
	var r audit.SkillUpdateReceipt
	if json.Unmarshal(b, &r) != nil || r.Tree != v.Tree || r.Commit != v.Commit || r.Source.Root != v.Root || r.Source.Repo != v.Repo || r.Source.Ref != v.Ref || r.Source.Entry != v.Entry || r.Source.SourceDigest != v.SourceDigest {
		return false
	}
	return audit.VerifySkillUpdateSource(r.Source, o.Home) == nil
}
