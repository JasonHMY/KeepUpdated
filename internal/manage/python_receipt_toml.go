package manage

import (
	"encoding/json"
	"errors"
	"strings"
)

// uv writes basic strings, arrays and inline tables. Accept only that generated
// subset, rejecting duplicate keys and unconsumed syntax instead of guessing TOML.
type receiptTOML struct {
	text string
	pos  int
}

func parseUVReceipt(data []byte) (map[string]any, error) {
	p := receiptTOML{text: string(data)}
	doc := map[string]any{}
	var table map[string]any
	for {
		p.space()
		if p.pos == len(p.text) {
			return doc, nil
		}
		if p.take('[') {
			start := p.pos
			for p.pos < len(p.text) && p.text[p.pos] != ']' {
				p.pos++
			}
			name := p.text[start:p.pos]
			if !p.take(']') || (name != "tool" && name != "tool.options") {
				return nil, errors.New("unsupported receipt table")
			}
			if _, exists := doc[name]; exists {
				return nil, errors.New("duplicate receipt table")
			}
			table = map[string]any{}
			doc[name] = table
		} else {
			if table == nil {
				return nil, errors.New("missing receipt table")
			}
			key, err := p.key()
			if err != nil {
				return nil, err
			}
			p.horizontal()
			if !p.take('=') {
				return nil, errors.New("invalid receipt assignment")
			}
			v, err := p.value(0)
			if err != nil {
				return nil, err
			}
			if _, exists := table[key]; exists {
				return nil, errors.New("duplicate receipt field")
			}
			table[key] = v
		}
		p.horizontal()
		if p.pos < len(p.text) && p.text[p.pos] == '#' {
			for p.pos < len(p.text) && p.text[p.pos] != '\n' {
				p.pos++
			}
		}
		if p.pos < len(p.text) && !p.take('\n') {
			return nil, errors.New("invalid receipt line")
		}
	}
}

func (p *receiptTOML) take(c byte) bool {
	if p.pos < len(p.text) && p.text[p.pos] == c {
		p.pos++
		return true
	}
	return false
}
func (p *receiptTOML) horizontal() {
	for p.pos < len(p.text) && strings.ContainsRune(" \t\r", rune(p.text[p.pos])) {
		p.pos++
	}
}
func (p *receiptTOML) space() {
	for p.pos < len(p.text) {
		if strings.ContainsRune(" \t\r\n", rune(p.text[p.pos])) {
			p.pos++
			continue
		}
		if p.text[p.pos] == '#' {
			for p.pos < len(p.text) && p.text[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		break
	}
}
func (p *receiptTOML) key() (string, error) {
	start := p.pos
	for p.pos < len(p.text) && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", rune(p.text[p.pos])) {
		p.pos++
	}
	if start == p.pos {
		return "", errors.New("unsupported receipt key")
	}
	return p.text[start:p.pos], nil
}
func (p *receiptTOML) value(depth int) (any, error) {
	p.space()
	if depth > 8 || p.pos >= len(p.text) {
		return nil, errors.New("invalid receipt value")
	}
	if p.take('"') {
		start := p.pos - 1
		for p.pos < len(p.text) {
			c := p.text[p.pos]
			p.pos++
			if c == '\\' {
				p.pos++
				continue
			}
			if c == '"' {
				var s string
				err := json.Unmarshal([]byte(p.text[start:p.pos]), &s)
				return s, err
			}
			if c == '\n' {
				break
			}
		}
		return nil, errors.New("invalid receipt string")
	}
	if p.take('[') {
		a := []any{}
		for {
			p.space()
			if p.take(']') {
				return a, nil
			}
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
			p.space()
			if p.take(']') {
				return a, nil
			}
			if !p.take(',') {
				return nil, errors.New("invalid receipt array")
			}
		}
	}
	if p.take('{') {
		m := map[string]any{}
		for {
			p.space()
			if p.take('}') {
				return m, nil
			}
			k, err := p.key()
			if err != nil {
				return nil, err
			}
			p.horizontal()
			if !p.take('=') {
				return nil, errors.New("invalid receipt inline table")
			}
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			if _, ok := m[k]; ok {
				return nil, errors.New("duplicate receipt key")
			}
			m[k] = v
			p.space()
			if p.take('}') {
				return m, nil
			}
			if !p.take(',') {
				return nil, errors.New("invalid receipt inline table")
			}
		}
	}
	for _, s := range []string{"true", "false"} {
		if strings.HasPrefix(p.text[p.pos:], s) {
			p.pos += len(s)
			return s == "true", nil
		}
	}
	return nil, errors.New("unsupported receipt value")
}
