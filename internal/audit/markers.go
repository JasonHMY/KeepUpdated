package audit

import (
	"regexp"
	"strings"
)

type markerParser struct {
	tokens  []string
	pos     int
	env     map[string]string
	invalid bool
}
type truth struct{ value, known bool }

var markerToken = regexp.MustCompile(`^\s*(>=|<=|==|!=|~=|>|<|\(|\)|"[^"]*"|'[^']*'|[A-Za-z_][A-Za-z0-9_]*)`)

func evaluateMarker(raw string, env map[string]string) (bool, bool) {
	if len(raw) > 8192 {
		return false, false
	}
	p := markerParser{env: env}
	for strings.TrimSpace(raw) != "" {
		m := markerToken.FindStringSubmatch(raw)
		if m == nil {
			return false, false
		}
		p.tokens = append(p.tokens, m[1])
		if len(p.tokens) > 256 {
			return false, false
		}
		raw = raw[len(m[0]):]
	}
	v := p.or()
	if p.invalid || p.pos != len(p.tokens) {
		return false, false
	}
	return v.value, v.known
}
func (p *markerParser) take(s string) bool {
	if p.pos < len(p.tokens) && p.tokens[p.pos] == s {
		p.pos++
		return true
	}
	return false
}
func (p *markerParser) or() truth {
	a := p.and()
	for p.take("or") {
		b := p.and()
		if a.known && a.value || b.known && b.value {
			a = truth{true, true}
		} else if a.known && b.known {
			a = truth{false, true}
		} else {
			a = truth{}
		}
	}
	return a
}
func (p *markerParser) and() truth {
	a := p.atom()
	for p.take("and") {
		b := p.atom()
		if a.known && !a.value || b.known && !b.value {
			a = truth{false, true}
		} else if a.known && b.known {
			a = truth{true, true}
		} else {
			a = truth{}
		}
	}
	return a
}
func (p *markerParser) atom() truth {
	if p.take("(") {
		v := p.or()
		if !p.take(")") {
			p.invalid = true
			return truth{}
		}
		return v
	}
	if p.pos+2 >= len(p.tokens) {
		p.pos = len(p.tokens)
		p.invalid = true
		return truth{}
	}
	left := p.tokens[p.pos]
	op := p.tokens[p.pos+1]
	p.pos += 2
	if op == "not" {
		if !p.take("in") {
			p.invalid = true
			return truth{}
		}
		op = "not in"
	}
	if p.pos >= len(p.tokens) {
		p.invalid = true
		return truth{}
	}
	right := p.tokens[p.pos]
	p.pos++
	switch op {
	case "==", "!=", ">", "<", ">=", "<=", "~=", "in", "not in":
	default:
		p.invalid = true
		return truth{}
	}
	a, ak, av := p.value(left)
	b, bk, bv := p.value(right)
	if !ak || !bk {
		return truth{}
	}
	if op == "in" || op == "not in" {
		v := strings.Contains(b, a)
		if op == "not in" {
			v = !v
		}
		return truth{v, true}
	}
	if av || bv {
		if av && bv {
			return truth{}
		}
		if !av {
			switch op {
			case "~=":
				return truth{}
			case ">":
				op = "<"
			case "<":
				op = ">"
			case ">=":
				op = "<="
			case "<=":
				op = ">="
			}
			a, b = b, a
		}
		v, k := pythonConstraint(a, op+b)
		return truth{v, k}
	}
	if numericVersion.MatchString(a) && numericVersion.MatchString(b) {
		v, k := pythonConstraint(a, op+b)
		return truth{v, k}
	}
	switch op {
	case "==":
		return truth{a == b, true}
	case "!=":
		return truth{a != b, true}
	case ">":
		return truth{a > b, true}
	case "<":
		return truth{a < b, true}
	case ">=":
		return truth{a >= b, true}
	case "<=":
		return truth{a <= b, true}
	default:
		return truth{}
	}
}
func (p *markerParser) value(s string) (string, bool, bool) {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') {
		return s[1 : len(s)-1], true, false
	}
	switch s {
	case "python_version", "python_full_version", "os_name", "sys_platform", "platform_release", "platform_system", "platform_version", "platform_machine", "platform_python_implementation", "implementation_name", "implementation_version", "extra", "extras", "dependency_groups":
	default:
		p.invalid = true
		return "", false, false
	}
	v, ok := p.env[s]
	return v, ok, s == "python_version" || s == "python_full_version" || s == "implementation_version"
}
