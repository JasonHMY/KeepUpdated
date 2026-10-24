package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/mcpref"
)

// PackageReference is a configuration-only migration, never an installed version claim.
type PackageReference struct {
	Launcher string `json:"launcher"`
	Before   string `json:"before"`
	After    string `json:"after"`
}

type stringToken struct {
	value      string
	start, end int
	quote      byte
}

func PlanReference(item audit.Item, replacement string) (Target, error) {
	target, err := Plan(item)
	if err != nil {
		return Target{}, err
	}
	data, _, _, err := readConfig(target.ConfigPath)
	if err != nil || digest(data) != target.SHA256 {
		return Target{}, errors.New("config_changed_since_plan")
	}
	ref, _, err := packageToken(data, target)
	if err != nil {
		return Target{}, err
	}
	if _, err = ref.Replacement(replacement); err != nil {
		return Target{}, err
	}
	target.Operation = ReplacePackageReference
	target.PackageReference = &PackageReference{Launcher: ref.Launcher, Before: ref.Value, After: replacement}
	return target, nil
}

func replaceReference(data []byte, target Target) ([]byte, error) {
	ref, token, err := packageToken(data, target)
	if err != nil {
		return nil, err
	}
	preview := target.PackageReference
	if ref.Launcher != preview.Launcher || ref.Value != preview.Before {
		return nil, errors.New("package_reference_changed")
	}
	value, err := ref.Replacement(preview.After)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(value)
	if token.quote == '\'' {
		encoded = []byte("'" + value + "'")
	}
	updated := append([]byte(nil), data[:token.start]...)
	updated = append(updated, encoded...)
	updated = append(updated, data[token.end:]...)
	next, _, err := packageToken(updated, target)
	if err != nil || next.Value != preview.After || next.Package != ref.Package || next.Launcher != ref.Launcher {
		return nil, errors.New("config_edit_verification_failed")
	}
	return updated, nil
}

func packageToken(data []byte, target Target) (mcpref.Reference, stringToken, error) {
	var command string
	var args []stringToken
	var err error
	if target.Schema == "toml" {
		command, args, err = tomlCommand(data, target)
	} else {
		command, args, err = jsonCommand(data, target)
	}
	if err != nil {
		return mcpref.Reference{}, stringToken{}, err
	}
	values := make([]string, len(args))
	for n, t := range args {
		values[n] = t.value
	}
	ref, err := mcpref.Parse(command, values)
	if err != nil {
		return ref, stringToken{}, err
	}
	return ref, args[ref.Index], nil
}

func jsonCommand(data []byte, target Target) (string, []stringToken, error) {
	root, err := parseJSONC(data)
	if err != nil {
		return "", nil, err
	}
	node := root.lookup(target.JSONPath)
	if node == nil || node.kind != '{' || node.lookup([]string{"url"}) != nil || node.lookup([]string{"serverUrl"}) != nil {
		return "", nil, errors.New("local_launcher_definition_required")
	}
	command := node.lookup([]string{"command"})
	args := node.lookup([]string{"args"})
	if command == nil {
		return "", nil, errors.New("missing_command")
	}
	var tokens []stringToken
	decode := func(n *jsonNode) (stringToken, error) {
		var value string
		if n == nil || n.kind != '"' || json.Unmarshal(data[n.start:n.end], &value) != nil {
			return stringToken{}, errors.New("static_string_required")
		}
		return stringToken{value, n.start, n.end, '"'}, nil
	}
	var cmd stringToken
	if command.kind == '[' {
		if args != nil || len(command.elements) == 0 {
			return "", nil, errors.New("ambiguous_command_arguments")
		}
		cmd, err = decode(command.elements[0])
		if err != nil {
			return "", nil, err
		}
		for _, n := range command.elements[1:] {
			token, e := decode(n)
			if e != nil {
				return "", nil, e
			}
			tokens = append(tokens, token)
		}
	} else {
		cmd, err = decode(command)
		if err != nil {
			return "", nil, err
		}
		if args == nil || args.kind != '[' {
			return "", nil, errors.New("static_arguments_required")
		}
		for _, n := range args.elements {
			token, e := decode(n)
			if e != nil {
				return "", nil, e
			}
			tokens = append(tokens, token)
		}
	}
	return cmd.value, tokens, nil
}

// shortcut: migrations accept single-line TOML strings/arrays only; extend the tokenizer before adding multiline forms.
func tomlCommand(data []byte, target Target) (string, []stringToken, error) {
	if _, err := removeTOMLServer(data, target.ServerKey); err != nil {
		return "", nil, err
	}
	selected := false
	pos := 0
	seen := map[string]bool{}
	command := ""
	var args []stringToken
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		text := strings.TrimSpace(string(line))
		if strings.HasPrefix(text, "[") {
			path, array, ok := tomlHeader(text)
			if !ok || array {
				return "", nil, errors.New("toml_table_syntax_unsupported")
			}
			selected = reflect.DeepEqual(path, []string{"mcp_servers", target.ServerKey})
		} else if selected && text != "" && !strings.HasPrefix(text, "#") {
			eq := bytes.IndexByte(line, '=')
			if eq < 0 {
				return "", nil, errors.New("toml_assignment_unsupported")
			}
			key, ok := tomlKey(strings.TrimSpace(string(line[:eq])))
			if !ok || seen[key] {
				return "", nil, errors.New("ambiguous_config_keys")
			}
			seen[key] = true
			if key == "url" || key == "serverUrl" {
				return "", nil, errors.New("local_launcher_definition_required")
			}
			if key == "command" || key == "args" {
				tokens, err := tomlStringTokens(line[eq+1:], pos+eq+1, key == "args")
				if err != nil {
					return "", nil, err
				}
				if key == "command" {
					command = tokens[0].value
				} else {
					args = tokens
				}
			}
		}
		pos += len(line)
	}
	if command == "" || !seen["args"] {
		return "", nil, errors.New("static_arguments_required")
	}
	return command, args, nil
}

func tomlStringTokens(data []byte, offset int, array bool) ([]stringToken, error) {
	pos := 0
	skip := func() {
		for pos < len(data) && strings.ContainsRune(" \t\r\n", rune(data[pos])) {
			pos++
		}
	}
	bad := errors.New("toml_static_string_or_array_required")
	skip()
	if array {
		if pos >= len(data) || data[pos] != '[' {
			return nil, bad
		}
		pos++
	}
	var tokens []stringToken
	for {
		skip()
		if array && pos < len(data) && data[pos] == ']' {
			pos++
			break
		}
		if pos >= len(data) || data[pos] != '"' && data[pos] != '\'' {
			return nil, bad
		}
		start := pos
		quote := data[pos]
		pos++
		for pos < len(data) && data[pos] != quote {
			if quote == '"' && data[pos] == '\\' {
				pos++
			}
			pos++
		}
		if pos >= len(data) {
			return nil, bad
		}
		pos++
		raw := data[start:pos]
		value := ""
		if quote == '\'' {
			value = string(raw[1 : len(raw)-1])
		} else if json.Unmarshal(raw, &value) != nil {
			return nil, bad
		}
		tokens = append(tokens, stringToken{value, offset + start, offset + pos, quote})
		skip()
		if !array {
			break
		}
		if pos >= len(data) {
			return nil, bad
		}
		if data[pos] == ']' {
			pos++
			break
		}
		if data[pos] != ',' {
			return nil, bad
		}
		pos++
	}
	skip()
	if pos < len(data) && data[pos] != '#' {
		return nil, bad
	}
	return tokens, nil
}
