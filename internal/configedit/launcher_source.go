package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/mcpref"
)

// LauncherSource contains only static, non-secret evidence. Raw env/args never
// leave the selected definition reader, and no client or launcher is executed.
type LauncherSource struct {
	Reference    mcpref.Reference
	ConfigSHA256 string
	Identity     string
}

// NPMLauncherSource supports local Codex TOML definitions with an explicit cwd
// and explicit npm config paths. Other clients have unproven environment
// inheritance; their existing exact-reference migration remains available.
func NPMLauncherSource(item audit.Item) (LauncherSource, error) {
	var source LauncherSource
	target, err := Plan(item)
	if err != nil {
		return source, err
	}
	data, _, _, err := readConfig(target.ConfigPath)
	if err != nil || digest(data) != target.SHA256 {
		return source, errors.New("mcp_launcher_config_changed")
	}
	ref, _, err := packageToken(data, target)
	if err != nil {
		return source, errors.New("mcp_launcher_constraints_unsupported")
	}
	source.Reference = ref
	source.ConfigSHA256 = target.SHA256
	if ref.Launcher != "npx" {
		return source, errors.New("mcp_launcher_source_manual")
	}
	if target.Schema != "toml" || filepath.Base(target.ConfigPath) != "config.toml" || filepath.Base(filepath.Dir(target.ConfigPath)) != ".codex" || target.Scope != "user-or-explicit-project" {
		return source, errors.New("mcp_launcher_environment_unknown")
	}
	command, _, err := tomlCommand(data, target)
	if err != nil || !staticAbsolute(command) {
		return source, errors.New("mcp_launcher_source_manual")
	}
	cwd, env, err := codexLauncherEnvironment(data, target)
	if err != nil {
		return source, err
	}
	if !staticAbsolute(cwd) {
		return source, errors.New("mcp_launcher_cwd_unknown")
	}
	realCWD, cwdErr := filepath.EvalSymlinks(cwd)
	if cwdErr != nil {
		return source, errors.New("mcp_launcher_cwd_unknown")
	}
	if st, err := os.Stat(realCWD); err != nil || !st.IsDir() {
		return source, errors.New("mcp_launcher_cwd_unknown")
	}
	for key, value := range env {
		switch key {
		case "HOME", "NPM_CONFIG_USERCONFIG", "NPM_CONFIG_GLOBALCONFIG":
			if !staticAbsolute(value) {
				return source, errors.New("mcp_launcher_environment_unknown")
			}
		case "PATH":
			for _, dir := range strings.Split(value, string(os.PathListSeparator)) {
				if !staticAbsolute(dir) {
					return source, errors.New("mcp_launcher_environment_unknown")
				}
			}
		default:
			return source, errors.New("mcp_launcher_environment_unknown")
		}
	}
	for _, key := range []string{"HOME", "NPM_CONFIG_USERCONFIG", "NPM_CONFIG_GLOBALCONFIG"} {
		if env[key] == "" {
			return source, errors.New("mcp_launcher_environment_unknown")
		}
	}
	// The npx script must belong to an npm distribution, not an arbitrary wrapper.
	// Reading launcher metadata is not an installed-server ownership claim.
	launcher, err := filepath.EvalSymlinks(command)
	if err != nil || filepath.Base(launcher) != "npx-cli.js" || filepath.Base(filepath.Dir(launcher)) != "bin" {
		return source, errors.New("mcp_launcher_source_manual")
	}
	npmRoot := filepath.Dir(filepath.Dir(launcher))
	metadata, _, _, err := readConfig(filepath.Join(npmRoot, "package.json"))
	var npm struct{ Name, Version string }
	if err != nil || json.Unmarshal(metadata, &npm) != nil || npm.Name != "npm" {
		return source, errors.New("mcp_launcher_source_manual")
	}
	major := strings.Split(npm.Version, ".")[0]
	if major != "7" && major != "8" && major != "9" && major != "10" && major != "11" {
		return source, errors.New("mcp_launcher_source_manual")
	}
	script, launcherStat, _, err := readConfig(launcher)
	if err != nil || launcherStat.Mode().Perm()&0111 == 0 {
		return source, errors.New("mcp_launcher_source_manual")
	}
	// Bind every ancestor, rather than guessing npm's project/workspace root.
	// Accepting only empty/comment/default-registry rc files makes that distinction irrelevant.
	paths := []string{filepath.Join(npmRoot, "npmrc"), env["NPM_CONFIG_USERCONFIG"], env["NPM_CONFIG_GLOBALCONFIG"]}
	for dir := realCWD; ; dir = filepath.Dir(dir) {
		paths = append(paths, filepath.Join(dir, ".npmrc"))
		if dir == filepath.Dir(dir) {
			break
		}
	}
	prefix := ""
	var evidence bytes.Buffer
	fmt.Fprintln(&evidence, "codex-local-npx-npm7-11-default-v2", target.SHA256, launcher, digest(script), digest(metadata), cwd, realCWD)
	for _, path := range paths {
		_, statErr := os.Lstat(path)
		if os.IsNotExist(statErr) {
			fmt.Fprintln(&evidence, path, "absent")
			continue
		}
		content, _, real, readErr := readConfig(path)
		if statErr != nil || readErr != nil {
			return source, errors.New("mcp_launcher_source_manual")
		}
		if !defaultNPMConfig(content, path == filepath.Join(npmRoot, "npmrc")) {
			return source, errors.New("mcp_launcher_source_manual")
		}
		if path == filepath.Join(npmRoot, "npmrc") {
			prefix = npmBuiltinPrefix(content)
		}
		fmt.Fprintln(&evidence, path, real, digest(content))
	}
	lookup, err := npmLookupIdentity(realCWD, env["HOME"], prefix, ref.Package)
	if err != nil {
		return source, err
	}
	fmt.Fprintln(&evidence, lookup)
	source.Identity = "npm-default:" + digest(evidence.Bytes())
	return source, nil
}

func staticAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n$`~")
}

func defaultNPMConfig(data []byte, builtin bool) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return false
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if key == "registry" && value == mcpref.DefaultNPMRegistry {
			continue
		}
		// Official npm distributions can set their prefix here. The selected
		// definition explicitly overrides both config paths, so it cannot redirect them.
		if builtin && key == "prefix" && staticAbsolute(value) {
			continue
		}
		return false
	}
	return true
}

func codexLauncherEnvironment(data []byte, target Target) (string, map[string]string, error) {
	env := map[string]string{}
	cwd := ""
	var table []string
	seen := map[string]bool{}
	seenTables := map[string]bool{}
	bad := errors.New("mcp_launcher_environment_unknown")
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		text := strings.TrimSpace(string(line))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			path, array, ok := tomlHeader(text)
			if !ok || array {
				return "", nil, bad
			}
			table = path
			if len(path) >= 2 && path[0] == "mcp_servers" && path[1] == target.ServerKey {
				tableKey := strings.Join(path, "\x00")
				if seenTables[tableKey] {
					return "", nil, bad
				}
				seenTables[tableKey] = true
			}
			if len(path) > 2 && path[0] == "mcp_servers" && path[1] == target.ServerKey && !reflect.DeepEqual(path, []string{"mcp_servers", target.ServerKey, "env"}) {
				return "", nil, bad
			}
			continue
		}
		if len(table) < 2 || table[0] != "mcp_servers" || table[1] != target.ServerKey {
			continue
		}
		eq := bytes.IndexByte(line, '=')
		if eq < 0 {
			return "", nil, bad
		}
		key, ok := tomlKey(strings.TrimSpace(string(line[:eq])))
		identity := strings.Join(table, ".") + "." + key
		if !ok || seen[identity] {
			return "", nil, bad
		}
		seen[identity] = true
		if len(table) == 3 {
			tokens, err := tomlStringTokens(line[eq+1:], 0, false)
			if err != nil || len(tokens) != 1 {
				return "", nil, bad
			}
			env[key] = tokens[0].value
		} else {
			switch key {
			case "cwd":
				tokens, err := tomlStringTokens(line[eq+1:], 0, false)
				if err != nil || len(tokens) != 1 {
					return "", nil, bad
				}
				cwd = tokens[0].value
			case "env_vars":
				tokens, err := tomlStringTokens(line[eq+1:], 0, true)
				if err != nil || len(tokens) != 0 {
					return "", nil, bad
				}
			case "env": // Inline env is deliberately outside the bounded TOML reader.
				return "", nil, bad
			}
		}
	}
	return cwd, env, nil
}

func launcherSourceMatches(target Target) bool {
	if target.LauncherSourceIdentity == "" {
		return true
	}
	item := audit.Item{ID: target.ItemID, Kind: "mcp-server", Name: target.ServerKey, Path: url.PathEscape(target.ConfigPath) + "#" + url.PathEscape(target.Scope) + "#" + url.PathEscape(target.ServerKey)}
	source, err := NPMLauncherSource(item)
	return err == nil && source.Identity == target.LauncherSourceIdentity
}
