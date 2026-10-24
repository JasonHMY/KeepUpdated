package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type mcpGroup struct {
	scope   string
	servers map[string]any
}

func defaultAgentConfigs(home string) []string {
	var paths []string
	for _, rel := range []string{
		".codex/config.toml", ".claude.json", ".claude/settings.json",
		"Library/Application Support/Claude/claude_desktop_config.json", ".cursor/mcp.json",
		"Library/Application Support/Code/User/mcp.json", "Library/Application Support/Code/User/settings.json",
		".config/opencode/opencode.json", ".config/opencode/opencode.jsonc",
		".openclaw/openclaw.json", ".hermes/config.yaml", ".hermes/config.yml",
	} {
		paths = append(paths, filepath.Join(home, rel))
	}
	return paths
}

func (s *Scanner) agentConfigPaths() []string {
	return append(defaultAgentConfigs(s.Config.Home), s.extraSourcePaths(s.Config.MCPConfigs, "mcp", false)...)
}

func (s *Scanner) extraSourcePaths(paths []string, adapter string, directory bool) []string {
	var out []string
	for _, p := range paths {
		q, ok := localSourcePath(s.Config.Home, p)
		if !ok {
			s.diag(adapter, "", "source_path_requires_absolute_path")
			continue
		}
		st, err := os.Stat(q)
		if err != nil {
			code := "source_path_unreadable"
			if os.IsNotExist(err) {
				code = "source_path_missing"
			}
			s.diag(adapter, q, code)
		} else if st.IsDir() != directory {
			s.diag(adapter, q, "source_path_type_mismatch")
			continue
		}
		out = append(out, q)
	}
	return out
}

func localSourcePath(home, p string) (string, bool) {
	if p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) || strings.ContainsAny(p, "\x00\r\n") {
		return "", false
	}
	return filepath.Clean(p), true
}

func (s *Scanner) agentSkillRoots() []string {
	var roots []string
	for _, rel := range []string{".config/opencode/skills", ".openclaw/skills", ".hermes/skills"} {
		roots = append(roots, filepath.Join(s.Config.Home, rel))
	}
	for _, project := range s.Config.Projects {
		if p, err := filepath.Abs(project); err == nil {
			for _, rel := range []string{".opencode/skills", ".agents/skills", ".claude/skills", ".codex/skills"} {
				roots = append(roots, filepath.Join(p, rel))
			}
		}
	}
	configs := defaultAgentConfigs(s.Config.Home)
	for _, p := range s.Config.MCPConfigs {
		if q, ok := localSourcePath(s.Config.Home, p); ok {
			configs = append(configs, q)
		}
	}
	for _, config := range uniq(configs) {
		b, err := readSmall(config)
		if err != nil {
			continue
		}
		var extra []string
		switch strings.ToLower(filepath.Ext(config)) {
		case ".yaml", ".yml":
			_, extra, _ = yamlAgentSources(string(b))
		default:
			var m map[string]any
			if json.Unmarshal(stripJSONComments(b), &m) == nil {
				if skills, ok := m["skills"].(map[string]any); ok {
					if load, ok := skills["load"].(map[string]any); ok {
						extra = stringList(load["extraDirs"])
					}
				}
			}
		}
		for _, p := range extra {
			if q, ok := localSourcePath(s.Config.Home, p); ok {
				roots = append(roots, q)
			} else {
				s.diag("skills", config, "skill_root_requires_absolute_path")
			}
		}
	}
	return append(roots, s.extraSourcePaths(s.Config.SkillRoots, "skills", true)...)
}

// The two mcp shapes belong to different clients: OpenCode's server map and OpenClaw's servers map.
func jsonAgentServers(m map[string]any) ([]mcpGroup, bool) {
	var groups []mcpGroup
	invalid := false
	add := func(value any, scope string) {
		if servers, ok := value.(map[string]any); ok {
			groups = append(groups, mcpGroup{scope, servers})
		} else {
			invalid = true
		}
	}
	for _, key := range []string{"mcpServers", "servers"} {
		if value, present := m[key]; present {
			scope := "user-or-explicit-project"
			if key == "servers" && m["mcpServers"] != nil {
				scope = "servers"
			}
			add(value, scope)
		}
	}
	addMCP := func(value any, scope string) {
		if obj, ok := value.(map[string]any); ok {
			if servers, present := obj["servers"]; present {
				// A server named "servers" in OpenCode can itself have a command.
				if entry, ok := servers.(map[string]any); ok && (entry["command"] != nil || entry["type"] != nil || entry["url"] != nil) {
					if scope == "" {
						scope = "mcp"
					}
					add(obj, scope)
				} else {
					if scope == "" {
						scope = "mcp.servers"
					}
					add(servers, scope)
				}
			} else {
				if scope == "" {
					scope = "mcp"
				}
				add(obj, scope)
			}
		} else {
			invalid = true
		}
	}
	if value, present := m["mcp"]; present {
		addMCP(value, "")
	}
	if value, present := m["nodeHost"]; present {
		if node, ok := value.(map[string]any); ok {
			if value, present := node["mcp"]; present {
				addMCP(value, "nodeHost.mcp.servers")
			}
		} else {
			invalid = true
		}
	}
	// Claude's local project scopes must retain their own identities.
	if value, present := m["projects"]; present {
		if projects, ok := value.(map[string]any); ok {
			for project, value := range projects {
				if obj, ok := value.(map[string]any); ok {
					if servers, present := obj["mcpServers"]; present {
						add(servers, project)
					}
				}
			}
		}
	}
	return groups, invalid
}

func staticCommand(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", true
	case string:
		return v, true
	case []any:
		if len(v) == 0 {
			return "", false
		}
		for _, arg := range v {
			if _, ok := arg.(string); !ok {
				return "", false
			}
		}
		return str(v[0]), true
	default:
		return "", false
	}
}

// shortcut: Hermes YAML is a static block-map subset; use a full parser before supporting aliases or rewrites.
func yamlAgentSources(text string) (map[string]any, []string, bool) {
	servers := map[string]any{}
	var roots []string
	section, external, sectionPartial := "", false, false
	var current map[string]any
	argumentList := false
	partial := false
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 4096), 4<<20)
	for sc.Scan() {
		raw := sc.Text()
		line := yamlSourceLine(strings.TrimSpace(raw))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))], "\t") {
			partial = true
			if current != nil {
				current["_parse_error"] = true
			}
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		key, value, pair := yamlSourcePair(line)
		if indent == 0 {
			section, external, sectionPartial, current = "", false, false, nil
			argumentList = false
			if pair && (key == "mcp_servers" || key == "skills") {
				section = key
				if value != "" && value != "{}" {
					partial = true
					sectionPartial = true
				}
			} else if !pair && line != "---" {
				partial = true
			}
			continue
		}
		if section == "skills" {
			if indent == 2 {
				external = pair && key == "external_dirs"
				if external && value != "" {
					var list []string
					if json.Unmarshal([]byte(value), &list) == nil {
						roots = append(roots, list...)
					} else {
						partial = true
					}
					external = false
				}
			} else if external && indent == 4 && strings.HasPrefix(line, "- ") {
				if path, ok := yamlSourceScalar(strings.TrimSpace(line[2:])); ok {
					roots = append(roots, path)
				} else {
					partial = true
				}
			} else if external {
				partial = true
			}
			continue
		}
		if section != "mcp_servers" {
			continue
		}
		if indent == 2 {
			current = nil
			argumentList = false
			if !pair || key == "" || key == "<<" {
				partial = true
				continue
			}
			current = map[string]any{}
			_, duplicate := servers[key]
			servers[key] = current
			if value != "" || sectionPartial || duplicate {
				current["_parse_error"] = true
				partial = true
			}
			continue
		}
		if current == nil {
			partial = true
			continue
		}
		// YAML permits args list items at the same indentation as the args key.
		if argumentList && indent == 4 && (line == "-" || strings.HasPrefix(line, "- ")) {
			continue
		}
		if indent != 4 {
			continue // Nested arguments, headers and environment values are never inspected.
		}
		argumentList = pair && key == "args" && value == ""
		if !pair || key == "<<" {
			partial = true
			current["_parse_error"] = true
			continue
		}
		switch key {
		case "env", "environment":
			current["_environment_unparsed"] = true
		case "command", "url", "serverUrl":
			if _, duplicate := current[key]; duplicate {
				partial = true
				current["_parse_error"] = true
			}
			if scalar, ok := yamlSourceScalar(value); ok {
				current[key] = scalar
			} else {
				partial = true
				current["_parse_error"] = true
			}
		case "enabled", "disabled":
			if value == "true" || value == "false" {
				current[key] = value == "true"
			} else {
				partial = true
				current["_parse_error"] = true
			}
		}
	}
	return servers, roots, partial || sc.Err() != nil
}

func yamlSourceLine(line string) string {
	quote := byte(0)
	for n := 0; n < len(line); n++ {
		c := line[n]
		if quote != 0 {
			if c == quote && (quote == '\'' || n == 0 || line[n-1] != '\\') {
				quote = 0
			}
		} else if c == '\'' || c == '"' {
			quote = c
		} else if c == '#' && (n == 0 || line[n-1] == ' ') {
			return strings.TrimSpace(line[:n])
		}
	}
	return line
}

func yamlSourcePair(line string) (string, string, bool) {
	quote := byte(0)
	for n := 0; n < len(line); n++ {
		c := line[n]
		if quote != 0 {
			if c == quote && (quote == '\'' || n == 0 || line[n-1] != '\\') {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
		} else if c == ':' && (n+1 == len(line) || line[n+1] == ' ') {
			key, ok := yamlSourceScalar(strings.TrimSpace(line[:n]))
			return key, strings.TrimSpace(line[n+1:]), ok
		}
	}
	return "", "", false
}

func yamlSourceScalar(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, "\"") {
		var scalar string
		if json.Unmarshal([]byte(value), &scalar) == nil {
			return scalar, true
		}
		return "", false
	}
	if strings.HasPrefix(value, "'") {
		if len(value) >= 2 && strings.HasSuffix(value, "'") {
			body := value[1 : len(value)-1]
			if !strings.Contains(strings.ReplaceAll(body, "''", ""), "'") {
				return strings.ReplaceAll(body, "''", "'"), true
			}
		}
		return "", false
	}
	if strings.ContainsAny(value, "[]{}&*!|>\"'\t") || strings.HasPrefix(value, "#") {
		return "", false
	}
	if n := strings.Index(value, " #"); n >= 0 {
		value = strings.TrimSpace(value[:n])
	}
	return value, value != ""
}

// Relate only a stable executable entry to a declared package bin or manager installation.
// A versioned Cellar/store path would keep pointing at an old payload after its owner updates.
func (s *Scanner) relateMCPCommand(server *Item, entry, configuredCommand string) {
	entry = filepath.Clean(entry)
	command := real(entry)
	if command == "" || genericMCPLauncher(filepath.Base(entry)) || genericMCPLauncher(filepath.Base(command)) {
		return
	}
	for _, candidate := range s.Report.Items {
		if candidate.Kind == "node-package" {
			matched, launcher := nodeCommandBin(candidate, entry, command, s.Config)
			if launcher {
				server.Related = nil
				return
			}
			if matched {
				server.Related = append(server.Related, candidate.ID)
			}
		} else if pythonMCPBin(candidate, entry, command, s.Config) {
			server.Related = append(server.Related, candidate.ID)
		}
	}
	if len(server.Related) == 0 {
		for _, candidate := range s.Report.Items {
			if (candidate.Kind == "formula" || candidate.Kind == "cask") && s.brewCommandRoot(candidate, entry, command) {
				server.Related = append(server.Related, candidate.ID)
			}
		}
	}
	server.Related = uniq(server.Related)
	if len(server.Related) == 1 {
		server.Update = result("unsupported", "use_installation_owner")
	}
	if len(server.Related) > 0 {
		lookup := "absolute"
		if !filepath.IsAbs(configuredCommand) {
			lookup = "path"
			server.mcpSearchPath = s.Config.Path
		}
		server.Evidence = append(server.Evidence, "command-linked-to-installed-owner", "command-entry:"+entry, "command-lookup:"+lookup)
	}
}

func genericMCPLauncher(name string) bool {
	switch strings.ToLower(name) {
	case "node", "nodejs", "python", "pythonw", "bash", "zsh", "sh", "dash", "fish", "csh", "tcsh", "env", "npx", "npm", "npx-cli.js", "npm-cli.js", "pnpm", "uvx", "uv", "bunx", "bun", "pipx", "java", "javaw", "ruby", "perl", "php", "deno", "dotnet", "swift", "osascript", "arch", "timeout", "open", "xcrun":
		return true
	}
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "python") && strings.Trim(name[len("python"):], "0123456789.") == ""
}

func nodeCommandBin(candidate Item, entry, command string, config Config) (matched, launcher bool) {
	if candidate.RealPath == "" || !within(command, candidate.RealPath) || candidate.Identity != candidate.Name {
		return false, false
	}
	var metadata struct {
		Name, Version string
		Bin           any
	}
	b, err := readSmall(filepath.Join(candidate.Path, "package.json"))
	if err != nil || json.Unmarshal(b, &metadata) != nil || metadata.Name != candidate.Name || metadata.Version != candidate.Version {
		return false, false
	}
	bins := map[string]string{}
	switch value := metadata.Bin.(type) {
	case string:
		bins[filepath.Base(candidate.Name)] = value
	case map[string]any:
		for name, value := range value {
			if target, ok := value.(string); ok {
				bins[name] = target
			}
		}
	}
	root := filepath.Dir(candidate.Path)
	if strings.HasPrefix(candidate.Name, "@") {
		root = filepath.Dir(root)
	}
	if filepath.Base(root) != "node_modules" {
		return false, false
	}
	// A direct script or another alias can still be the same declared launcher.
	for name, target := range bins {
		if genericMCPLauncher(name) && !filepath.IsAbs(target) && within(filepath.Join(candidate.Path, target), candidate.Path) && real(filepath.Join(candidate.Path, target)) == command {
			return false, true
		}
	}
	for name, target := range bins {
		if name == "" || filepath.Base(name) != name || filepath.IsAbs(target) || !within(filepath.Join(candidate.Path, target), candidate.Path) {
			continue
		}
		declared := filepath.Join(candidate.Path, target)
		if real(declared) != command {
			continue
		}
		if entry == declared {
			return true, false
		}
		var exported []string
		switch candidate.Owner {
		case "npm":
			if filepath.Base(filepath.Dir(root)) == "lib" {
				exported = append(exported, filepath.Join(filepath.Dir(filepath.Dir(root)), "bin", name))
			}
		case "pnpm":
			exported = append(exported, filepath.Join(config.Home, "Library/pnpm", name), filepath.Join(config.Home, ".local/share/pnpm", name))
		case "bun":
			exported = append(exported, filepath.Join(config.Home, ".bun/bin", name))
		}
		if candidate.Owner == "pnpm" && validManagerRoot(config.ManagerRoots["pnpm"]) && filepath.Dir(filepath.Dir(root)) == config.ManagerRoots["pnpm"] {
			exported = []string{filepath.Join(filepath.Dir(config.ManagerRoots["pnpm"]), name)}
		}
		if candidate.Owner == "pnpm" || candidate.Owner == "bun" {
			if bin := config.ManagerBins[candidate.Owner]; validManagerRoot(bin) {
				exported = []string{filepath.Join(bin, name)}
			}
		}
		for _, path := range exported {
			if entry == path {
				return true, false
			}
		}
	}
	return false, false
}

func (s *Scanner) brewCommandRoot(candidate Item, entry, command string) bool {
	if candidate.RealPath == "" || !within(command, candidate.RealPath) {
		return false
	}
	for _, root := range s.Config.BrewRoots {
		rack := "Cellar"
		if candidate.Kind == "cask" {
			rack = "Caskroom"
		}
		if candidate.Path != filepath.Join(root, rack, candidate.Name) {
			continue
		}
		stable := filepath.Dir(entry) == filepath.Join(root, "bin") || filepath.Dir(entry) == filepath.Join(root, "sbin")
		if candidate.Kind == "formula" {
			if candidate.ActiveVersion == "" || candidate.ActiveVersion != candidate.Version || !within(command, filepath.Join(candidate.RealPath, candidate.ActiveVersion)) {
				continue
			}
			stable = stable || within(entry, filepath.Join(root, "opt", candidate.Name))
		}
		if stable {
			return true
		}
	}
	return false
}
