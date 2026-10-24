package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

func (s *Scanner) configs() {
	paths := s.agentConfigPaths()
	for _, p := range s.Config.Projects {
		q, e := filepath.Abs(p)
		if e != nil {
			continue
		}
		for _, f := range []string{".mcp.json", ".cursor/mcp.json", ".vscode/mcp.json", ".codex/config.toml", "opencode.json", "opencode.jsonc"} {
			paths = append(paths, filepath.Join(q, f))
		}
	}
	for _, p := range uniq(paths) {
		b, e := readSmall(p)
		if os.IsNotExist(e) {
			continue
		}
		i := item("mcp-config", filepath.Base(filepath.Dir(p)), p, "configuration")
		i.Source = "local-config"
		i.Update = result("unsupported", "configuration_not_update_target")
		if e != nil {
			i.Health = result("error", "config_unreadable")
			s.add(i)
			continue
		}
		var groups []mcpGroup
		partial := false
		partialCode := "toml_subset_partial"
		switch strings.ToLower(filepath.Ext(p)) {
		case ".toml":
			servers, incomplete := tomlServers(string(b))
			partial = incomplete
			groups = append(groups, mcpGroup{"user-or-explicit-project", servers})
		case ".yaml", ".yml":
			servers, _, _ := yamlAgentSources(string(b))
			groups = append(groups, mcpGroup{"mcp_servers", servers})
			partial = true
			partialCode = "yaml_subset_partial"
		default:
			var m map[string]any
			if json.Unmarshal(stripJSONComments(b), &m) != nil {
				i.Health = result("error", "config_parse_failed")
				if filepath.Base(p) == "openclaw.json" || strings.EqualFold(filepath.Ext(p), ".json5") {
					i.Health = result("unknown", "json5_syntax_unsupported_or_invalid")
				}
				s.add(i)
				s.diag("mcp", p, i.Health.Code)
				continue
			}
			var invalidMap bool
			groups, invalidMap = jsonAgentServers(m)
			if invalidMap {
				i.Health = result("error", "invalid_mcp_server_map")
				s.diag("mcp", p, "invalid_mcp_server_map")
			}
		}
		if i.Health.Status != "error" {
			i.Health = result("ok", "config_parsed_static")
			if len(groups) == 0 {
				i.Health = result("unknown", "mcp_schema_not_found")
			}
			if partial {
				i.Health = result("unknown", partialCode)
				s.diag("mcp", p, partialCode)
			}
		}
		s.add(i)
		configHash := fmt.Sprintf("%x", sha256.Sum256(b))
		for _, group := range groups {
			for n, c := range group.servers {
				s.server(p, group.scope, n, c, configHash)
			}
		}
	}
}
func (s *Scanner) server(config, scope, name string, v any, configHash string) {
	i := item("mcp-server", name, url.PathEscape(config)+"#"+url.PathEscape(scope)+"#"+url.PathEscape(name), "configuration")
	i.Evidence = append(i.Evidence, "config:"+config, "scope:"+scope)
	if configHash != "" {
		i.Evidence = append(i.Evidence, "config-sha256:"+configHash)
	}
	i.Update = result("unsupported", "server_source_not_verified")
	m, ok := v.(map[string]any)
	if !ok {
		i.Health = result("error", "invalid_server_entry")
		s.add(i)
		return
	}
	if m["_parse_error"] == true {
		i.Health = result("unknown", "server_syntax_unsupported")
		s.add(i)
		return
	}
	enabled, hasEnabled := m["enabled"].(bool)
	disabled, _ := m["disabled"].(bool)
	if disabled || hasEnabled && !enabled {
		i.Source = "local-command"
		if str(m["url"]) != "" || str(m["serverUrl"]) != "" {
			i.Source = "remote"
			i.Update = result("unsupported", "remote_not_locally_managed")
			i.Evidence = append(i.Evidence, mcpEndpointEvidence(m))
		} else if command, health := s.boundMCPCommand(m); health.Code == "command_resolved_not_started" {
			i.RealPath = real(command)
			configured, _ := staticCommand(m["command"])
			s.relateMCPCommand(&i, command, configured)
		}
		if i.Source == "local-command" {
			classifyMCPLauncher(&i, m)
		}
		i.Evidence = append(i.Evidence, "disabled")
		i.Health = result("unknown", "disabled_not_started")
		s.add(i)
		return
	}
	if str(m["url"]) != "" || str(m["serverUrl"]) != "" {
		i.Source = "remote"
		i.Evidence = append(i.Evidence, mcpEndpointEvidence(m))
		i.Health = result("unknown", "remote_not_contacted")
		i.Update = result("unsupported", "remote_not_locally_managed")
		s.add(i)
		return
	}
	i.Source = "local-command"
	classifyMCPLauncher(&i, m)
	p, health := s.boundMCPCommand(m)
	i.Health = health
	if p != "" {
		i.RealPath = real(p)
		i.Evidence = append(i.Evidence, "command-basename:"+filepath.Base(p))
		base := filepath.Base(p)
		if base == "npx" || base == "uvx" || base == "bunx" {
			i.Evidence = append(i.Evidence, "launcher-may-download-on-start")
		}
		if health.Code == "command_resolved_not_started" {
			configured, _ := staticCommand(m["command"])
			s.relateMCPCommand(&i, p, configured)
		}
	}
	// Arguments, environment values, headers and URLs are deliberately excluded.
	s.add(i)
}

func mcpEndpointEvidence(definition map[string]any) string {
	address := str(definition["url"])
	alternative := str(definition["serverUrl"])
	if address == "" {
		address = alternative
	} else if alternative != "" && address != alternative {
		return "endpoint:unknown"
	}
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return "endpoint:unknown"
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "$%\\ ") {
		return "endpoint:unknown"
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "endpoint:unknown"
		}
	}
	if host == "localhost" && !strings.HasPrefix(u.Host, "[") {
		return "endpoint:loopback"
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return "endpoint:unknown"
		}
		if ip.Unmap().IsLoopback() {
			return "endpoint:loopback"
		}
	} else {
		if strings.Contains(u.Host, "[") || strings.Trim(host, ".0123456789") == "" {
			return "endpoint:unknown"
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
				return "endpoint:unknown"
			}
		}
	}
	return "endpoint:remote"
}

func (s *Scanner) staticMCPCommand(value any) (string, Result) {
	command, validCommand := staticCommand(value)
	if !validCommand {
		return "", result("unknown", "command_syntax_unsupported")
	}
	if command == "" {
		return "", result("error", "missing_command")
	}
	if strings.ContainsAny(command, "\n\r$`|;&<>") {
		return "", result("unknown", "command_requires_expansion")
	}
	p := command
	if !filepath.IsAbs(p) {
		if strings.ContainsRune(command, filepath.Separator) {
			return "", result("unknown", "command_relative_path_requires_review")
		}
		p = ""
		for _, d := range filepath.SplitList(s.Config.Path) {
			if !filepath.IsAbs(d) {
				return "", result("unknown", "command_environment_path_requires_review")
			}
			q := filepath.Join(d, command)
			if st, e := os.Stat(q); e == nil && !st.IsDir() && st.Mode().Perm()&0111 != 0 {
				p = q
				break
			}
		}
	}
	if p == "" || real(p) == "" {
		return "", result("error", "command_missing_or_inaccessible")
	}
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() || syscall.Access(p, 1) != nil {
		return p, result("error", "command_not_executable")
	}
	return p, result("unknown", "command_resolved_not_started")
}

var table = regexp.MustCompile(`^\[mcp_servers\.(?:"([^"]+)"|'([^']+)'|([A-Za-z0-9_-]+))(\.env)?\]$`)

const tomlKeyPartPattern = `(?:[A-Za-z0-9_-]+|"(?:[^"\\\x00-\x1f]|\\.)*"|'[^'\x00-\x1f]*')`

var tomlFieldKey = regexp.MustCompile(`^` + tomlKeyPartPattern + `(?:[ \t]*\.[ \t]*` + tomlKeyPartPattern + `)*$`)
var tomlKeyParts = regexp.MustCompile(tomlKeyPartPattern)

func tomlServers(text string) (map[string]any, bool) {
	servers := map[string]any{}
	var current map[string]any
	partial := false
	unparsedTable := false
	rootTable := false
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			rootTable = line == "[mcp_servers]"
			current = nil
			if m := table.FindStringSubmatch(line); m != nil {
				n := m[1] + m[2] + m[3]
				previous, _ := servers[n].(map[string]any)
				if m[4] != "" {
					if previous == nil {
						previous = map[string]any{}
						servers[n] = previous
					}
					// The subset reader does not resolve client environment semantics.
					previous["_environment_unparsed"] = true
					continue
				}
				current = map[string]any{}
				if previous["_environment_unparsed"] == true {
					current["_environment_unparsed"] = true
				}
				servers[n] = current
			} else if strings.Contains(line, "mcp_servers") && line != "[mcp_servers]" && !strings.HasSuffix(line, ".http_headers]") {
				partial = true
				unparsedTable = true
			}
			continue
		}
		if rootTable && line != "" && !strings.HasPrefix(line, "#") {
			partial = true
			if key, _, ok := strings.Cut(line, "="); ok {
				key = strings.Trim(strings.TrimSpace(key), "\"'")
				if key != "" {
					servers[key] = map[string]any{"_parse_error": true}
				}
			}
			continue
		}
		if current == nil && strings.HasPrefix(line, "mcp_servers") && strings.Contains(line, "=") {
			partial = true
		}
		if current == nil || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			partial = true
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		validKey := tomlFieldKey.MatchString(k)
		parts := tomlKeyParts.FindAllString(k, -1)
		for n, part := range parts {
			if strings.HasPrefix(part, "\"") {
				if json.Unmarshal([]byte(part), &parts[n]) != nil {
					validKey = false
				}
			} else if strings.HasPrefix(part, "'") {
				parts[n] = part[1 : len(part)-1]
			}
		}
		if !validKey {
			partial = true
			current["_parse_error"] = true
			continue
		}
		key, dotted := parts[0], len(parts) > 1
		if dotted || key == "env" || key == "environment" || key == "env_vars" && v != "[]" {
			current["_environment_unparsed"] = true
		}
		if dotted {
			continue
		}
		k = key
		if k == "command" || k == "args" || k == "url" || k == "enabled" {
			if _, duplicate := current[k]; duplicate {
				partial = true
				current["_parse_error"] = true
				continue
			}
		}
		if k == "args" {
			var args []any
			valid := json.Unmarshal([]byte(v), &args) == nil && args != nil
			for _, arg := range args {
				if _, ok := arg.(string); !ok {
					valid = false
				}
			}
			if valid {
				current[k] = args
			} else {
				partial = true
				current["_parse_error"] = true
			}
			continue
		}
		if k != "command" && k != "url" && k != "enabled" {
			continue
		}
		if k == "enabled" {
			b, e := strconv.ParseBool(v)
			if e != nil {
				partial = true
				current["_parse_error"] = true
			} else {
				current[k] = b
			}
			continue
		}
		if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
			current[k] = strings.Trim(v, "'")
			continue
		}
		var val string
		if json.Unmarshal([]byte(v), &val) == nil {
			current[k] = val
		} else {
			partial = true
			current["_parse_error"] = true
		}
	}
	if sc.Err() != nil {
		partial = true
	}
	if unparsedTable {
		for _, value := range servers {
			value.(map[string]any)["_environment_unparsed"] = true
		}
	}
	return servers, partial
}
func stripJSONComments(b []byte) []byte {
	out := append([]byte(nil), b...)
	in, escape := false, false
	for n := 0; n < len(out); n++ {
		c := out[n]
		if in {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				in = false
			}
			continue
		}
		if c == '"' {
			in = true
			continue
		}
		if c == '/' && n+1 < len(out) {
			if out[n+1] == '/' {
				for n < len(out) && out[n] != '\n' {
					out[n] = ' '
					n++
				}
			} else if out[n+1] == '*' {
				closed := false
				out[n] = ' '
				n++
				out[n] = ' '
				for n+1 < len(out) {
					n++
					if out[n] == '*' && n+1 < len(out) && out[n+1] == '/' {
						out[n] = ' '
						n++
						out[n] = ' '
						closed = true
						break
					}
					if out[n] != '\n' {
						out[n] = ' '
					}
				}
				if !closed {
					return []byte{0} // An unterminated comment is not valid JSONC.
				}
			}
		}
	}
	// JSONC trailing commas, outside quoted strings only.
	in = false
	escape = false
	for n, c := range out {
		if in {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				in = false
			}
			continue
		}
		if c == '"' {
			in = true
		}
		if c == ',' {
			j := n + 1
			for j < len(out) && strings.ContainsRune(" \t\r\n", rune(out[j])) {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				previous := n - 1
				for previous >= 0 && strings.ContainsRune(" \t\r\n", rune(out[previous])) {
					previous--
				}
				if previous < 0 || strings.ContainsRune("{[,:", rune(out[previous])) {
					return []byte{0}
				}
				out[n] = ' '
			}
		}
	}
	return out
}

var skillBackupDirectory = regexp.MustCompile(`^\..+\.bak-(?:[0-9]{14}|[A-Za-z][A-Za-z0-9_-]*-[0-9]{8})$`)

func (s *Scanner) skills() {
	h := s.Config.Home
	plugins, registryErr := ReadClaudePluginRegistry(h)
	roots := []string{filepath.Join(h, ".codex/skills"), filepath.Join(h, ".agents/skills"), filepath.Join(h, ".claude/skills"), filepath.Join(h, ".codex/plugins/cache"), filepath.Join(h, ".claude/plugins/cache")}
	roots = append(roots, s.agentSkillRoots()...)
	addSkill := func(p, owner string) {
		i := item("skill", filepath.Base(filepath.Dir(p)), p, owner)
		i.Source = "local"
		i.Evidence = append(i.Evidence, "entry:SKILL.md")
		b, e := readSmall(p)
		if e != nil {
			i.Health = result("error", "skill_entry_unreadable")
		} else if !strings.HasPrefix(string(b), "---") {
			i.Health = result("unknown", "frontmatter_missing")
		} else {
			i.Health = result("ok", "entry_readable")
		}
		resolvedSkill := real(p)
		if strings.Contains(p, string(os.PathSeparator)+".system"+string(os.PathSeparator)) || strings.Contains(resolvedSkill, string(os.PathSeparator)+".system"+string(os.PathSeparator)) {
			i.Owner = "system"
		}
		if i.Owner == "system" {
			i.Update = result("unsupported", "system_skill_update_client")
		} else if strings.Contains(owner, "plugin") || within(resolvedSkill, filepath.Join(h, ".claude/plugins/cache")) || within(resolvedSkill, filepath.Join(h, ".codex/plugins/cache")) {
			i.Source = "plugin-cache"
			s.pluginSkill(&i, plugins)
		} else {
			i.Update = result("unknown", "skill_upstream_unknown")
			s.gitState(&i)
		}
		s.classifyPluginCache(&i, plugins, registryErr)
		s.add(i)
	}
	visiting := map[string]bool{}
	var walk func(string, string, int)
	walk = func(root, owner string, depth int) {
		if depth > 14 {
			s.diag("skills", root, "depth_limit")
			return
		}
		rp := real(root)
		if rp == "" {
			if _, e := os.Lstat(root); e == nil {
				s.diag("skills", root, "broken_link")
			} else if !os.IsNotExist(e) {
				s.diag("skills", root, "unreadable_root")
			}
			return
		}
		key := owner + ":" + rp
		if visiting[key] {
			return
		}
		visiting[key] = true
		defer delete(visiting, key)
		err := filepath.WalkDir(rp, func(p string, d fs.DirEntry, e error) error {
			if s.Config.Context != nil && s.Config.Context.Err() != nil {
				return fs.SkipAll
			}
			relPath, _ := filepath.Rel(rp, p)
			p = filepath.Join(root, relPath)
			if e != nil {
				s.diag("skills", p, "unreadable_path")
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if strings.Count(rel, string(os.PathSeparator))+depth > 14 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == ".git" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".keepupdated-skill-") || skillBackupDirectory.MatchString(d.Name()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				entry := filepath.Join(p, "SKILL.md")
				if info, err := os.Lstat(entry); err == nil && !info.IsDir() {
					addSkill(entry, owner)
					return filepath.SkipDir
				}
			}
			if d.Type()&os.ModeSymlink != 0 {
				r := real(p)
				if r == "" {
					s.diag("skills", p, "broken_link")
				} else if st, e := os.Stat(p); e == nil && st.IsDir() {
					walk(p, owner, depth+1)
					return nil
				}
				if d.Name() != "SKILL.md" {
					return nil
				}
			}
			if d.Name() != "SKILL.md" {
				return nil
			}
			addSkill(p, owner)
			return nil
		})
		if err != nil {
			s.diag("skills", root, "walk_failed")
		}
	}
	for _, p := range uniq(roots) {
		owner := filepath.Base(filepath.Dir(p))
		if strings.Contains(p, "/plugins/cache") {
			owner = "plugin-cache"
		}
		walk(p, owner, 0)
	}
}
func (s *Scanner) gitState(i *Item) {
	p := real(i.Path)
	if p == "" {
		return
	}
	if st, e := os.Stat(p); e == nil && !st.IsDir() {
		p = filepath.Dir(p)
	}
	root := p
	for {
		if exists(filepath.Join(root, ".git")) {
			break
		}
		next := filepath.Dir(root)
		if next == root || root == s.Config.Home {
			return
		}
		root = next
	}
	args := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", root}
	head, e := s.run("/usr/bin/git", append(args, "rev-parse", "--verify", "HEAD")...)
	if e != nil {
		i.Evidence = append(i.Evidence, "git-state-unreadable")
		return
	}
	i.Source = "git"
	i.Evidence = append(i.Evidence, "git-head:"+strings.TrimSpace(string(head)))
	if b, e := s.run("/usr/bin/git", append(args, "symbolic-ref", "--quiet", "--short", "HEAD")...); e == nil {
		i.Channel = "branch"
		i.Evidence = append(i.Evidence, "git-branch:"+strings.TrimSpace(string(b)))
	} else {
		i.Channel = "detached"
	}
	rel, e := filepath.Rel(root, p)
	if e != nil {
		return
	}
	b, e := s.run("/usr/bin/git", append(args, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=all", "--", rel)...)
	if e != nil {
		i.Evidence = append(i.Evidence, "git-dirty-unknown")
	} else if len(b) > 0 {
		i.Evidence = append(i.Evidence, "git-local-modifications")
	} else {
		i.Evidence = append(i.Evidence, "git-worktree-clean")
	}
	i.Update = result("unknown", "git_remote_not_fetched")
}
