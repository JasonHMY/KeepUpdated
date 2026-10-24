// Package configedit edits one bound MCP configuration definition without starting its server.
package configedit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

const maxConfigBytes = 4 << 20

type Operation string

const ReplacePackageReference Operation = "replace-package-reference"

type Target struct {
	LauncherSourceIdentity string            `json:"launcher_source_identity,omitempty"`
	Operation              Operation         `json:"operation,omitempty"`
	PackageReference       *PackageReference `json:"package_reference,omitempty"`
	ItemID                 string            `json:"item_id"`
	ConfigPath             string            `json:"config_path"`
	RealPath               string            `json:"real_path"`
	SHA256                 string            `json:"sha256"`
	Scope                  string            `json:"scope"`
	ServerKey              string            `json:"server_key"`
	Schema                 string            `json:"schema"`
	JSONPath               []string          `json:"json_path,omitempty"`
	FileMode               uint32            `json:"file_mode"`
}

type Result struct {
	ContentSHA256 string `json:"-"`
	Changed       bool   `json:"changed"`
	ConfigPath    string `json:"config_path"`
	BackupPath    string `json:"backup_path,omitempty"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
}

// Plan binds the original file and exact scope/key. No configuration values leave this package.
func Plan(item audit.Item) (Target, error) {
	if item.Kind != "mcp-server" {
		return Target{}, errors.New("not_mcp_server")
	}
	parts := strings.Split(item.Path, "#")
	if len(parts) != 3 {
		return Target{}, errors.New("invalid_mcp_locator")
	}
	for n := range parts {
		part, err := url.PathUnescape(parts[n])
		if err != nil || strings.ContainsAny(part, "\x00\r\n") {
			return Target{}, errors.New("invalid_mcp_locator")
		}
		parts[n] = part
	}
	if !filepath.IsAbs(parts[0]) || parts[1] == "" || parts[2] == "" || item.Name != parts[2] {
		return Target{}, errors.New("invalid_mcp_locator")
	}
	target := Target{ItemID: item.ID, ConfigPath: filepath.Clean(parts[0]), Scope: parts[1], ServerKey: parts[2]}
	data, st, real, err := readConfig(target.ConfigPath)
	if err != nil {
		return Target{}, err
	}
	target.RealPath, target.FileMode, target.SHA256 = real, uint32(st.Mode().Perm()), digest(data)
	_, schema, path, err := removeDefinition(data, target)
	if err != nil {
		return Target{}, err
	}
	target.Schema, target.JSONPath = schema, path
	return target, nil
}

// Apply accepts only a matching current file and re-derives the allowed MCP location.
// The caller owns user confirmation and must display the bound plan before calling Apply.
func Apply(target Target) (Result, error) { return ApplyWithBackup(target, nil) }

// ApplyWithBackup reports the private backup before replacing configuration.
func ApplyWithBackup(target Target, checkpoint func(string) error) (Result, error) {
	result := Result{ConfigPath: target.ConfigPath, Status: "failed"}
	fail := func(code string) (Result, error) {
		result.Reason = code
		return result, errors.New(code)
	}
	if (target.PackageReference != nil) != (target.Operation == ReplacePackageReference) || target.Operation != "" && target.Operation != ReplacePackageReference {
		return fail("invalid_config_operation")
	}
	if !filepath.IsAbs(target.ConfigPath) || target.SHA256 == "" || target.RealPath == "" || strings.ContainsAny(target.ConfigPath, "\x00\r\n") {
		return fail("invalid_config_binding")
	}
	original, st, real, err := readConfig(target.ConfigPath)
	if err != nil {
		return fail(err.Error())
	}
	if real != target.RealPath || uint32(st.Mode().Perm()) != target.FileMode || digest(original) != target.SHA256 {
		return fail("config_changed_since_plan")
	}
	if target.LauncherSourceIdentity != "" {
		if target.Operation != ReplacePackageReference {
			return fail("invalid_config_binding")
		}
		if !launcherSourceMatches(target) {
			return fail("mcp_launcher_source_changed")
		}
	}
	updated, schema, path, err := removeDefinition(original, target)
	if err == nil && target.PackageReference != nil {
		updated, err = replaceReference(original, target)
	}
	if err != nil {
		return fail(err.Error())
	}
	if schema != target.Schema || !reflect.DeepEqual(path, target.JSONPath) || bytes.Equal(original, updated) {
		return fail("invalid_config_binding")
	}
	dir := filepath.Dir(real)
	backup, err := os.CreateTemp(dir, "."+filepath.Base(real)+".keepupdated-backup-*")
	if err != nil {
		return fail("config_backup_failed")
	}
	result.BackupPath = backup.Name()
	if err := writeFile(backup, original, 0600); err != nil {
		_ = os.Remove(backup.Name())
		result.BackupPath = ""
		return fail("config_backup_failed")
	}
	if checkpoint != nil {
		if err := checkpoint(result.BackupPath); err != nil {
			return result, err
		}
	}
	staged, err := os.CreateTemp(dir, ".keepupdated-config-edit-*")
	if err != nil {
		return fail("config_stage_failed")
	}
	defer os.Remove(staged.Name())
	if err := writeFile(staged, updated, st.Mode().Perm()); err != nil {
		return fail("config_stage_failed")
	}
	if !launcherSourceMatches(target) {
		return fail("mcp_launcher_source_changed")
	}
	// Recheck immediately before the atomic replacement to reject an editor's intervening save.
	current, currentStat, currentReal, err := readConfig(target.ConfigPath)
	if err != nil || currentReal != real || !os.SameFile(st, currentStat) || digest(current) != target.SHA256 {
		return fail("config_changed_since_plan")
	}
	if err := os.Rename(staged.Name(), real); err != nil {
		return fail("config_replace_failed")
	}
	result.Changed = true
	actual, actualStat, actualReal, err := readConfig(target.ConfigPath)
	if err != nil || actualReal != real || actualStat.Mode().Perm() != st.Mode().Perm() || !bytes.Equal(actual, updated) {
		return fail("config_postverification_failed")
	}
	result.ContentSHA256 = digest(updated)
	result.Status, result.Reason = "applied", "mcp_definition_removed"
	if target.PackageReference != nil {
		result.Reason = "mcp_package_reference_changed"
	}
	return result, nil
}

func readConfig(path string) ([]byte, os.FileInfo, string, error) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, nil, "", errors.New("config_not_regular_or_unreadable")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, "", errors.New("config_not_regular_or_unreadable")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(st, actual) || actual.Size() > maxConfigBytes {
		return nil, nil, "", errors.New("config_not_regular_or_oversized")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return nil, nil, "", errors.New("config_unreadable_or_oversized")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, nil, "", errors.New("config_path_unresolved")
	}
	return data, actual, real, nil
}

func writeFile(file *os.File, data []byte, mode os.FileMode) error {
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func removeDefinition(data []byte, target Target) ([]byte, string, []string, error) {
	switch strings.ToLower(filepath.Ext(target.ConfigPath)) {
	case ".yaml", ".yml":
		return nil, "", nil, errors.New("yaml_config_edit_unsupported")
	case ".toml":
		if target.Scope != "user-or-explicit-project" {
			return nil, "", nil, errors.New("toml_scope_unsupported")
		}
		updated, err := removeTOMLServer(data, target.ServerKey)
		return updated, "toml", nil, err
	default:
		updated, path, err := removeJSONServer(data, target.Scope, target.ServerKey)
		return updated, "jsonc", path, err
	}
}
