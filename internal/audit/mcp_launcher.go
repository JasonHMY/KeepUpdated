package audit

import (
	"path/filepath"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/mcpref"
)

func launcherReference(m map[string]any) (mcpref.Reference, bool) {
	command, ok := staticCommand(m["command"])
	if !ok {
		return mcpref.Reference{}, false
	}
	values := m["args"]
	if list, ok := m["command"].([]any); ok {
		if m["args"] != nil {
			return mcpref.Reference{}, false
		}
		values = list[1:]
	}
	var args []string
	if values != nil {
		list, ok := values.([]any)
		if !ok {
			return mcpref.Reference{}, false
		}
		for _, v := range list {
			a, ok := v.(string)
			if !ok {
				return mcpref.Reference{}, false
			}
			args = append(args, a)
		}
	}
	ref, err := mcpref.Parse(command, args)
	return ref, err == nil
}

func classifyMCPLauncher(i *Item, m map[string]any) {
	if ref, ok := launcherReference(m); ok {
		i.Update = result("unsupported", "launcher_reference_requires_explicit_migration")
		if ref.Version == "" || ref.Version == "latest" {
			i.Update = result("unsupported", "launcher_unpinned_not_persistently_managed")
		}
		i.Evidence = append(i.Evidence, "launcher-reference-recognized-not-installed")
	}
}

func (s *Scanner) boundMCPCommand(m map[string]any) (string, Result) {
	command, _ := staticCommand(m["command"])
	if value, present := m["args"]; present {
		args, valid := value.([]any)
		_, commandArray := m["command"].([]any)
		if !valid || commandArray {
			return "", result("unknown", "command_syntax_unsupported")
		}
		for _, arg := range args {
			if _, ok := arg.(string); !ok {
				return "", result("unknown", "command_syntax_unsupported")
			}
		}
	}
	if !filepath.IsAbs(command) {
		if m["_environment_unparsed"] == true {
			return "", result("unknown", "command_environment_path_requires_review")
		}
		for _, key := range []string{"env", "environment"} {
			if value, present := m[key]; present {
				env, ok := value.(map[string]any)
				if _, pathSet := env["PATH"]; !ok || pathSet {
					return "", result("unknown", "command_environment_path_requires_review")
				}
			}
		}
	}
	return s.staticMCPCommand(m["command"])
}

// MCPCommandOwnerMatches rechecks the scanned entry before delegating a plan.
// Older reports without a configuration/entry binding require a fresh scan.
func MCPCommandOwnerMatches(server, owner Item, report Report, configHash string, config Config) bool {
	entry, boundHash, lookup := "", "", ""
	seen := map[string]bool{}
	for _, e := range server.Evidence {
		key, value, _ := strings.Cut(e, ":")
		switch key {
		case "command-entry", "config-sha256", "command-lookup":
			if seen[key] {
				return false
			}
			seen[key] = true
		}
		switch key {
		case "command-entry":
			entry = value
		case "config-sha256":
			boundHash = value
		case "command-lookup":
			lookup = value
		}
	}
	if !filepath.IsAbs(entry) || filepath.Clean(entry) != entry || len(configHash) != 64 || boundHash != configHash || server.RealPath == "" || real(entry) != server.RealPath {
		return false
	}
	s := &Scanner{Config: config, Report: report}
	command := entry
	switch lookup {
	case "absolute":
	case "path":
		// Keep the scan's PATH private. A decoded report needs a fresh scan.
		if server.mcpSearchPath == "" {
			return false
		}
		command, s.Config.Path = filepath.Base(entry), server.mcpSearchPath
	default:
		return false
	}
	if resolved, health := s.staticMCPCommand(command); resolved != entry || health.Code != "command_resolved_not_started" {
		return false
	}
	var rebound Item
	s.relateMCPCommand(&rebound, entry, entry)
	return len(rebound.Related) == 1 && rebound.Related[0] == owner.ID
}
