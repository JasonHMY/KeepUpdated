package configedit

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type jsonMember struct {
	key   string
	start int
	value *jsonNode
	comma int
}

type jsonNode struct {
	kind     byte
	start    int
	end      int
	members  []jsonMember
	elements []*jsonNode
}

type jsonParser struct {
	data []byte
	pos  int
}

func parseJSONC(data []byte) (*jsonNode, error) {
	parser := jsonParser{data: data}
	node, err := parser.value(0)
	if err == nil {
		err = parser.skip()
		if parser.pos != len(data) {
			err = errors.New("unsupported_config_syntax")
		}
	}
	return node, err
}

func (p *jsonParser) skip() error {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		case '/':
			if p.pos+1 >= len(p.data) {
				return errors.New("unsupported_config_syntax")
			}
			switch p.data[p.pos+1] {
			case '/':
				p.pos += 2
				for p.pos < len(p.data) && p.data[p.pos] != '\n' {
					p.pos++
				}
			case '*':
				p.pos += 2
				for p.pos+1 < len(p.data) && !(p.data[p.pos] == '*' && p.data[p.pos+1] == '/') {
					p.pos++
				}
				if p.pos+1 >= len(p.data) {
					return errors.New("unsupported_config_syntax")
				}
				p.pos += 2
			default:
				return errors.New("unsupported_config_syntax")
			}
		default:
			return nil
		}
	}
	return nil
}

func (p *jsonParser) quoted() (string, error) {
	start := p.pos
	if start >= len(p.data) || p.data[start] != '"' {
		return "", errors.New("unsupported_config_syntax")
	}
	p.pos++
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case '\\':
			p.pos += 2
		case '"':
			p.pos++
			var value string
			if json.Unmarshal(p.data[start:p.pos], &value) != nil {
				return "", errors.New("unsupported_config_syntax")
			}
			return value, nil
		default:
			p.pos++
		}
	}
	return "", errors.New("unsupported_config_syntax")
}

func (p *jsonParser) value(depth int) (*jsonNode, error) {
	if depth > 128 || p.skip() != nil || p.pos >= len(p.data) {
		return nil, errors.New("unsupported_config_syntax")
	}
	node := &jsonNode{kind: p.data[p.pos], start: p.pos}
	switch node.kind {
	case '{':
		p.pos++
		seen := map[string]bool{}
		if p.skip() != nil {
			return nil, errors.New("unsupported_config_syntax")
		}
		if p.pos < len(p.data) && p.data[p.pos] == '}' {
			p.pos++
			break
		}
		for {
			start := p.pos
			key, err := p.quoted()
			if err != nil {
				return nil, err
			}
			if seen[key] {
				return nil, errors.New("ambiguous_config_keys")
			}
			seen[key] = true
			if p.skip() != nil || p.pos >= len(p.data) || p.data[p.pos] != ':' {
				return nil, errors.New("unsupported_config_syntax")
			}
			p.pos++
			value, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			member := jsonMember{key: key, start: start, value: value, comma: -1}
			if p.skip() != nil || p.pos >= len(p.data) {
				return nil, errors.New("unsupported_config_syntax")
			}
			if p.data[p.pos] == ',' {
				member.comma = p.pos
				p.pos++
				if p.skip() != nil || p.pos >= len(p.data) {
					return nil, errors.New("unsupported_config_syntax")
				}
				node.members = append(node.members, member)
				if p.data[p.pos] != '}' {
					continue
				}
			} else {
				node.members = append(node.members, member)
			}
			if p.data[p.pos] != '}' {
				return nil, errors.New("unsupported_config_syntax")
			}
			p.pos++
			break
		}
	case '[':
		p.pos++
		for {
			if p.skip() != nil || p.pos >= len(p.data) {
				return nil, errors.New("unsupported_config_syntax")
			}
			if p.data[p.pos] == ']' {
				p.pos++
				break
			}
			element, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			node.elements = append(node.elements, element)
			if p.skip() != nil || p.pos >= len(p.data) {
				return nil, errors.New("unsupported_config_syntax")
			}
			if p.data[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.data[p.pos] != ']' {
				return nil, errors.New("unsupported_config_syntax")
			}
			p.pos++
			break
		}
	case '"':
		if _, err := p.quoted(); err != nil {
			return nil, err
		}
	default:
		for p.pos < len(p.data) && !strings.ContainsRune(" \t\r\n,]}/", rune(p.data[p.pos])) {
			p.pos++
		}
		if !json.Valid(p.data[node.start:p.pos]) {
			return nil, errors.New("unsupported_config_syntax")
		}
	}
	node.end = p.pos
	return node, nil
}

func (n *jsonNode) lookup(path []string) *jsonNode {
	for _, key := range path {
		if n == nil || n.kind != '{' {
			return nil
		}
		var child *jsonNode
		for _, member := range n.members {
			if member.key == key {
				child = member.value
				break
			}
		}
		n = child
	}
	return n
}

func removeJSONServer(data []byte, scope, key string) ([]byte, []string, error) {
	root, err := parseJSONC(data)
	if err != nil {
		return nil, nil, err
	}
	var candidates [][]string
	switch scope {
	case "user-or-explicit-project":
		candidates = [][]string{{"mcpServers", key}, {"servers", key}, {"mcp", "servers", key}, {"mcp", key}}
	case "nodeHost.mcp.servers":
		candidates = [][]string{{"nodeHost", "mcp", "servers", key}}
	case "mcp", "servers", "mcpServers":
		candidates = [][]string{{scope, key}}
	case "mcp.servers":
		candidates = [][]string{{"mcp", "servers", key}}
	default:
		candidates = [][]string{{"projects", scope, "mcpServers", key}}
	}
	var path []string
	for _, candidate := range candidates {
		if root.lookup(candidate) != nil {
			if path != nil {
				return nil, nil, errors.New("ambiguous_mcp_definition")
			}
			path = candidate
		}
	}
	if path == nil {
		return nil, nil, errors.New("mcp_definition_not_found")
	}
	parent := root.lookup(path[:len(path)-1])
	for n, member := range parent.members {
		if member.key != key {
			continue
		}
		spans := []byteSpan{{member.start, member.value.end}}
		if member.comma >= 0 {
			spans = append(spans, byteSpan{member.comma, member.comma + 1})
		} else if n > 0 {
			comma := parent.members[n-1].comma
			if comma < 0 {
				return nil, nil, errors.New("unsupported_config_syntax")
			}
			spans = append(spans, byteSpan{comma, comma + 1})
		}
		updated := removeSpans(data, spans)
		after, err := parseJSONC(updated)
		if err != nil || after.lookup(path) != nil {
			return nil, nil, errors.New("config_edit_verification_failed")
		}
		return updated, path, nil
	}
	return nil, nil, errors.New("mcp_definition_not_found")
}

type byteSpan struct{ start, end int }

func removeSpans(data []byte, spans []byteSpan) []byte {
	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	out := make([]byte, 0, len(data))
	pos := 0
	for _, span := range spans {
		out = append(out, data[pos:span.start]...)
		pos = span.end
	}
	return append(out, data[pos:]...)
}
