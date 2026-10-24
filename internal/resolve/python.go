package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func (r Resolver) python(ctx context.Context, item audit.Item, binding manage.Binding) (manage.ResolvedTarget, error) {
	policy, err := r.pythonPolicy(ctx, item, binding, "update")
	if err != nil {
		return manage.ResolvedTarget{}, err
	}

	args := []string{"runpip", filepath.Base(binding.Scope), "list", "--outdated", "--format=json", "--disable-pip-version-check"}
	configPaths := []string{filepath.Join(r.Options.Home, "Library/Application Support/pip/pip.conf"), filepath.Join(r.Options.Home, ".config/pip/pip.conf"), filepath.Join(r.Options.Home, ".pip/pip.conf"), filepath.Join(binding.Scope, "pip.conf"), "/Library/Application Support/pip/pip.conf"}
	if item.Owner == "uv" {
		args = []string{"pip", "list", "--outdated", "--format=json", "--python", filepath.Join(binding.Scope, "bin/python"), "--no-python-downloads"}
		configPaths = []string{filepath.Join(r.Options.Home, ".config/uv/uv.toml"), filepath.Join(r.Options.Home, "uv.toml"), filepath.Join(r.Options.Home, "pyproject.toml"), "/etc/uv/uv.toml"}
	}
	if err := pythonConfigSupportsQuery(item.Owner, configPaths); err != nil {
		return manage.ResolvedTarget{}, err
	}
	data, err := r.query(ctx, item.Owner, binding, args...)
	if err != nil {
		return manage.ResolvedTarget{}, err
	}
	var rows []struct {
		Name, Version string
		Latest        string `json:"latest_version"`
	}
	if json.Unmarshal(data, &rows) != nil {
		return manage.ResolvedTarget{}, pythonQueryError(data)
	}
	version := ""
	for _, row := range rows {
		if normalizePython(row.Name) != normalizePython(item.Name) {
			continue
		}
		if version != "" {
			return manage.ResolvedTarget{}, errors.New("python owner query unavailable")
		}
		if row.Version != item.Version {
			return manage.ResolvedTarget{}, errors.New("python installation changed")
		}
		comparison, known := audit.CompareVersions(row.Version, row.Latest)
		if !known || comparison >= 0 {
			return manage.ResolvedTarget{}, errors.New("python candidate not a newer numeric release")
		}
		version = row.Latest
	}
	if version == "" && item.Owner == "pipx" {
		// pip omits packages when their index could not be read, even on exit 0.
		// Its --uptodate list provides positive equality evidence for this package.
		args[3] = "--uptodate"
		data, err = r.query(ctx, item.Owner, binding, args...)
		if err != nil {
			return manage.ResolvedTarget{}, err
		}
		rows = nil
		if json.Unmarshal(data, &rows) != nil {
			return manage.ResolvedTarget{}, pythonQueryError(data)
		}
		for _, row := range rows {
			if normalizePython(row.Name) != normalizePython(item.Name) {
				continue
			}
			if version != "" || row.Version != item.Version {
				return manage.ResolvedTarget{}, errors.New("python installation changed")
			}
			version = row.Version
		}
	}
	if version == "" {
		return manage.ResolvedTarget{}, errors.New("python candidate unavailable")
	}
	if err := manage.PythonTargetAllowed(item, binding, r.Options, version); err != nil {
		return manage.ResolvedTarget{}, err
	}
	digest, err := configDigest(item.Owner, configPaths)
	if err != nil {
		return manage.ResolvedTarget{}, err
	}
	return manage.ResolvedTarget{Python: policy, Version: version, Source: "manager-config:" + digest, OriginVerified: true, Reason: "configured_manager_source"}, nil
}

func normalizePython(name string) string {
	return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(name))
}

func pythonQueryError(data []byte) error {
	// pip can print an index failure before its JSON and still return exit zero.
	if strings.Contains(string(data), "Could not fetch URL ") {
		return manage.CommandFailure{Code: "owner_network_unavailable"}
	}
	return errors.New("python owner query unavailable")
}

func (r Resolver) pythonPolicy(ctx context.Context, item audit.Item, b manage.Binding, action string) (*manage.PythonPolicy, error) {
	data, err := r.query(ctx, item.Owner, b, "--version")
	if err != nil {
		return nil, err
	}
	v := strings.TrimSpace(string(data))
	if item.Owner == "uv" {
		parts := strings.Fields(v)
		if len(parts) < 2 || parts[0] != "uv" {
			return nil, manage.CommandFailure{Code: "python_manager_capability_unsupported"}
		}
		v = parts[1]
	}
	return manage.ReadPythonPolicy(item, b, r.Options, v, action)
}

// Outdated queries do not solve install-only constraints or uv receipt overrides.
// Do not let a digest turn those configured semantics into execution authority.
func pythonConfigSupportsQuery(owner string, paths []string) error {
	keys := `constraint|build-constraint|requirement|override-dependencies|constraint-dependencies|exclude-dependencies|build-constraint-dependencies|dependency-metadata|extra-build-dependencies|preview|preview-features|resolution|prerelease|exclude-newer|exclude-newer-package|no-binary|only-binary|no-deps|config-settings|python|python-preference|python-platform`
	if owner == "uv" {
		keys += `|pip|index|indexes|index-url|extra-index-url|default-index|find-links|no-index|index-strategy|sources`
	}
	pattern := regexp.MustCompile(`(?im)^\s*["']?(?:[a-z0-9_-]+\.)*(?:` + keys + `)["']?\s*(?:=|:)`)
	for _, path := range paths {
		data, err := readMetadata(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return manage.CommandFailure{Code: "owner_configuration_unreadable"}
		}
		if owner == "pipx" && !pipCandidateConfigSupportsQuery(data, keys) {
			return manage.CommandFailure{Code: "python_configuration_requires_review"}
		}
		if owner == "uv" && !uvCandidateConfigSyntax(data) {
			return manage.CommandFailure{Code: "python_configuration_requires_review"}
		}
		if pattern.Match(data) || owner == "uv" && regexp.MustCompile(`(?im)^\s*\[{1,2}\s*(?:["']?tool["']?\s*\.\s*["']?uv["']?\s*\.\s*)?["']?(?:index|indexes|sources)["']?\s*\]`).Match(data) {
			return manage.CommandFailure{Code: "python_configuration_requires_review"}
		}
	}
	return nil
}

// The key guard is not a TOML parser. Restrict its input to bare keys,
// bare table paths and single-line scalar values so quoted/dotted keys or
// nested inline tables cannot hide pip-only source settings from it.
func uvCandidateConfigSyntax(data []byte) bool {
	keyPattern := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	tablePattern := regexp.MustCompile(`^(?:\[[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*\]|\[\[[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*\]\])\s*(?:#.*)?$`)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !tablePattern.MatchString(line) {
				return false
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !keyPattern.MatchString(strings.TrimSpace(key)) {
			return false
		}
		value = strings.TrimSpace(value)
		if value == "" || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") || strings.HasPrefix(value, "\"\"\"") || strings.HasPrefix(value, "'''") {
			return false
		}
	}
	return true
}

// pip applies command sections after global defaults. Only global source options
// are shared by list and install; DEFAULT inheritance and command-specific
// sources remain manual. Track continuation indentation rather than interpreting
// a line inside an option value as a new section or option.
func pipCandidateConfigSupportsQuery(data []byte, restrictedKeys string) bool {
	sectionPattern := regexp.MustCompile(`^\[([A-Za-z0-9_-]+)\](?:[ \t]*[;#].*)?$`)
	keyPattern := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	section := ""
	haveOption, optionIndent := false, 0
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		left := strings.TrimLeft(raw, " \t")
		if !strings.HasPrefix(left, line) {
			return false
		}
		indent := len(raw) - len(left)
		if haveOption && indent > optionIndent {
			continue
		}
		if strings.HasPrefix(line, "[") {
			match := sectionPattern.FindStringSubmatch(line)
			if len(match) != 2 {
				return false
			}
			section = match[1]
			haveOption = false
			continue
		}
		delimiter := strings.IndexAny(line, "=:")
		if section == "" || delimiter <= 0 {
			return false
		}
		key := strings.TrimSpace(line[:delimiter])
		if !keyPattern.MatchString(key) {
			return false
		}
		// Match pip's option normalization, including long-option and underscore forms.
		key = strings.TrimPrefix(strings.ReplaceAll(strings.ToLower(key), "_", "-"), "--")
		if strings.Contains("|"+restrictedKeys+"|", "|"+key+"|") {
			return false
		}
		switch key {
		case "index-url", "extra-index-url", "find-links", "no-index":
			if section != "global" {
				return false
			}
		}
		haveOption, optionIndent = true, indent
	}
	return true
}
