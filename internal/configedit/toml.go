package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

type tomlTable struct {
	path  []string
	start int
	end   int
	array bool
}

func removeTOMLServer(data []byte, key string) ([]byte, error) {
	// Multiline strings can contain apparent table headers; never guess their boundaries.
	if bytes.Contains(data, []byte(`"""`)) || bytes.Contains(data, []byte(`'''`)) {
		return nil, errors.New("toml_multiline_edit_unsupported")
	}
	var tables []tomlTable
	pos := 0
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "[") {
			path, array, ok := tomlHeader(trimmed)
			if !ok {
				return nil, errors.New("toml_table_syntax_unsupported")
			}
			if len(tables) > 0 {
				tables[len(tables)-1].end = pos
			}
			tables = append(tables, tomlTable{path: path, start: pos, end: len(data), array: array})
		} else if len(tables) == 0 && strings.HasPrefix(trimmed, "mcp_servers") && !strings.HasPrefix(trimmed, "#") {
			return nil, errors.New("toml_inline_mcp_edit_unsupported")
		}
		pos += len(line)
	}
	var spans []byteSpan
	base := 0
	for _, table := range tables {
		if len(table.path) >= 2 && table.path[0] == "mcp_servers" && table.path[1] == key {
			if table.array {
				return nil, errors.New("toml_array_table_edit_unsupported")
			}
			if len(table.path) == 2 {
				base++
			}
			spans = append(spans, byteSpan{table.start, table.end})
		} else if len(table.path) == 1 && table.path[0] == "mcp_servers" {
			return nil, errors.New("toml_inline_mcp_edit_unsupported")
		}
	}
	if base == 0 {
		return nil, errors.New("mcp_definition_not_found")
	}
	if base != 1 {
		return nil, errors.New("ambiguous_mcp_definition")
	}
	return removeSpans(data, spans), nil
}

func tomlHeader(line string) ([]string, bool, bool) {
	quote := byte(0)
	end := -1
	for n := 1; n < len(line); n++ {
		c := line[n]
		if quote != 0 {
			if c == quote && (quote == '\'' || line[n-1] != '\\') {
				quote = 0
			}
		} else if c == '\'' || c == '"' {
			quote = c
		} else if c == ']' {
			end = n
			break
		}
	}
	if end < 0 {
		return nil, false, false
	}
	array := strings.HasPrefix(line, "[[")
	inner := line[1:end]
	tail := strings.TrimSpace(line[end+1:])
	if array {
		if !strings.HasPrefix(tail, "]") {
			return nil, false, false
		}
		inner = strings.TrimPrefix(inner, "[")
		tail = strings.TrimSpace(tail[1:])
	}
	if tail != "" && !strings.HasPrefix(tail, "#") {
		return nil, false, false
	}
	var path []string
	quote = 0
	start := 0
	for n := 0; n <= len(inner); n++ {
		if n < len(inner) {
			c := inner[n]
			if quote != 0 {
				if c == quote && (quote == '\'' || n == 0 || inner[n-1] != '\\') {
					quote = 0
				}
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				continue
			}
			if c != '.' {
				continue
			}
		}
		component, ok := tomlKey(strings.TrimSpace(inner[start:n]))
		if !ok {
			return nil, false, false
		}
		path = append(path, component)
		start = n + 1
	}
	return path, array, quote == 0 && len(path) > 0
}

func tomlKey(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	if key[0] == '"' {
		var out string
		if json.Unmarshal([]byte(key), &out) == nil && out != "" {
			return out, true
		}
		return "", false
	}
	if key[0] == '\'' {
		if len(key) >= 2 && key[len(key)-1] == '\'' && !strings.Contains(key[1:len(key)-1], "'") {
			return key[1 : len(key)-1], len(key) > 2
		}
		return "", false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "", false
		}
	}
	return key, true
}
