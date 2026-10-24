package audit

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// PluginOwner describes the registered disk version, not a running session.
type PluginOwner struct {
	Client  string `json:"client"`
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Root    string `json:"root"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

type ClaudePluginRecord struct {
	Scope        string `json:"scope"`
	InstallPath  string `json:"installPath"`
	Version      string `json:"version"`
	GitCommitSHA string `json:"gitCommitSha"`
	ProjectPath  string `json:"projectPath"`
}

var PluginID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*@[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ReadClaudePluginRegistry is static: it never invokes the client or loads plugins.
func ReadClaudePluginRegistry(home string) (map[string][]ClaudePluginRecord, error) {
	b, e := readSmall(filepath.Join(home, ".claude/plugins/installed_plugins.json"))
	if e != nil {
		return nil, e
	}
	var v struct {
		Version int                             `json:"version"`
		Plugins map[string][]ClaudePluginRecord `json:"plugins"`
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return nil, e
	}
	if v.Version != 2 {
		return nil, errors.New("unsupported plugin registry")
	}
	return v.Plugins, nil
}

func (s *Scanner) pluginSkill(i *Item, records map[string][]ClaudePluginRecord) {
	i.Update = result("unsupported", "plugin_owner_unverified")
	resolved := real(i.Path)
	if resolved == "" {
		return
	}
	if within(i.Path, filepath.Join(s.Config.Home, ".codex/plugins/cache")) || within(resolved, filepath.Join(real(s.Config.Home), ".codex/plugins/cache")) {
		i.Update = result("unsupported", "codex_plugin_update_in_client")
		return
	}
	var found *PluginOwner
	for id, rows := range records {
		if !PluginID.MatchString(id) {
			continue
		}
		parts := strings.Split(id, "@")
		for _, r := range rows {
			expected := filepath.Join(s.Config.Home, ".claude/plugins/cache", parts[1], parts[0], r.Version)
			if r.Version == "" || r.InstallPath != expected || !within(resolved, real(expected)) {
				continue
			}
			if found != nil {
				i.Update = result("unsupported", "plugin_owner_ambiguous")
				return
			}
			found = &PluginOwner{Client: "claude", ID: id, Scope: r.Scope, Root: r.InstallPath, Version: r.Version, Commit: r.GitCommitSHA}
		}
	}
	if found == nil {
		return
	}
	i.Plugin = found
	i.Version, i.Identity = found.Version, found.ID
	i.Update = result("unknown", "plugin_owner_candidate_required")
}

// PluginCache describes disk layout separately from authoritative registration.
type PluginCache struct {
	Client       string `json:"client"`
	Root         string `json:"root"`
	ID           string `json:"id"`
	Version      string `json:"version"`
	Registration string `json:"registration"`
}

func (s *Scanner) classifyPluginCache(i *Item, records map[string][]ClaudePluginRecord, registryErr error) {
	if i.Source != "plugin-cache" {
		return
	}
	resolved := real(i.Path)
	for _, client := range []string{"claude", "codex"} {
		cache := filepath.Join(s.Config.Home, "."+client, "plugins/cache")
		cache = real(cache)
		if cache == "" || resolved == "" || !within(resolved, cache) {
			continue
		}
		rel, err := filepath.Rel(cache, resolved)
		parts := strings.Split(rel, string(filepath.Separator))
		if err != nil || len(parts) < 4 {
			return
		}
		id := parts[1] + "@" + parts[0]
		if !PluginID.MatchString(id) {
			return
		}
		root := filepath.Join(cache, parts[0], parts[1], parts[2])
		state := "unverified"
		if i.Plugin != nil {
			state = "registered"
		} else if client == "claude" && registryErr == nil && completeClaudeRegistry(s.Config.Home, records) {
			state = "unregistered"
			// Conflicting records for this root are unknown, not proof it is historical.
			for _, rows := range records {
				for _, row := range rows {
					if real(row.InstallPath) == root {
						state = "unverified"
					}
				}
			}
		}
		i.PluginCache = &PluginCache{Client: client, Root: root, ID: id, Version: parts[2], Registration: state}
		i.Evidence = append(i.Evidence, "plugin-registration:"+state)
		if state == "unregistered" {
			i.Update = result("unsupported", "plugin_cache_unregistered")
		}
		return
	}
}

func completeClaudeRegistry(home string, records map[string][]ClaudePluginRecord) bool {
	if records == nil {
		return false
	}
	for id, rows := range records {
		if !PluginID.MatchString(id) || len(rows) == 0 {
			return false
		}
		parts := strings.Split(id, "@")
		for _, row := range rows {
			if row.Version == "" || row.Version == "." || row.Version == ".." || filepath.Base(row.Version) != row.Version || row.Scope == "" || row.InstallPath != filepath.Join(home, ".claude/plugins/cache", parts[1], parts[0], row.Version) {
				return false
			}
		}
	}
	return true
}
