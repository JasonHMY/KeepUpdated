package audit

import (
	"regexp"
	"strconv"
	"strings"
)

var numericVersion = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)
var comparisonTerm = regexp.MustCompile(`^(>=|<=|==|!=|>|<|=|~=)?\s*([0-9]+(?:\.[0-9]+)*(?:\.\*)?)$`)

// Numeric PEP 440 subset; epochs, pre/dev/post/local releases remain unknown.
func pythonConstraint(version, spec string) (bool, bool) {
	if len(spec) > 8192 || !numericVersion.MatchString(version) {
		return false, false
	}
	if _, ok := compare(version, version); !ok {
		return false, false
	}
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "(") && strings.HasSuffix(spec, ")") {
		spec = strings.TrimSpace(spec[1 : len(spec)-1])
	}
	if spec == "" {
		return true, true
	}
	matched := true
	for _, term := range strings.Split(spec, ",") {
		m := comparisonTerm.FindStringSubmatch(strings.TrimSpace(term))
		if m == nil || m[1] == "" || m[1] == "=" {
			return false, false
		}
		op, want := m[1], m[2]
		if strings.HasSuffix(want, ".*") {
			if op != "==" && op != "!=" {
				return false, false
			}
			prefix := strings.TrimSuffix(want, ".*")
			parts := strings.Split(version, ".")
			count := len(strings.Split(prefix, "."))
			if len(parts) > count {
				parts = parts[:count]
			}
			cmp, ok := compare(strings.Join(parts, "."), prefix)
			if !ok {
				return false, false
			}
			yes := cmp == 0
			if op == "!=" {
				yes = !yes
			}
			matched = matched && yes
			continue
		}
		cmp, known := compare(version, want)
		if !known {
			return false, false
		}
		yes := false
		switch op {
		case "==":
			yes = cmp == 0
		case "!=":
			yes = cmp != 0
		case ">=":
			yes = cmp >= 0
		case "<=":
			yes = cmp <= 0
		case ">":
			yes = cmp > 0
		case "<":
			yes = cmp < 0
		case "~=":
			parts := strings.Split(want, ".")
			if len(parts) < 2 {
				return false, false
			}
			upper, ok := increment(parts[:len(parts)-1])
			if !ok {
				return false, false
			}
			c, _ := compare(version, upper)
			yes = cmp >= 0 && c < 0
		}
		matched = matched && yes
	}
	return matched, true
}
func increment(parts []string) (string, bool) {
	out := append([]string(nil), parts...)
	if len(out) == 0 {
		return "", false
	}
	v, e := strconv.ParseUint(out[len(out)-1], 10, 32)
	if e != nil || v == 1<<32-1 {
		return "", false
	}
	out[len(out)-1] = strconv.FormatUint(v+1, 10)
	return strings.Join(out, "."), true
}

var nodeTerm = regexp.MustCompile(`^(>=|<=|>|<|=|\^|~)?v?([0-9xX*]+(?:\.[0-9xX*]+){0,2})$`)
var nodeOperatorSpace = regexp.MustCompile(`(>=|<=|>|<|=|\^|~)\s+`)
var nodeRelease = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func nodeConstraint(version, spec string) (bool, bool) {
	version = strings.TrimPrefix(version, "v")
	if len(spec) > 8192 || !nodeRelease.MatchString(version) {
		return false, false
	}
	if _, ok := compare(version, version); !ok {
		return false, false
	}
	unknown, anyMatch := false, false
	for _, alternative := range strings.Split(spec, "||") {
		alternative = strings.TrimSpace(alternative)
		if alternative == "" || alternative == "*" {
			anyMatch = true
			continue
		}
		if strings.Contains(alternative, " - ") {
			unknown = true
			continue
		}
		// Accept spaces between comparison operators and numeric operands.
		alternative = nodeOperatorSpace.ReplaceAllString(alternative, "$1")
		matched, known := true, true
		for _, term := range strings.Fields(alternative) {
			m := nodeTerm.FindStringSubmatch(term)
			if m == nil {
				known = false
				continue
			}
			op := m[1]
			parts := strings.Split(m[2], ".")
			exact := []string{}
			wild := false
			for _, p := range parts {
				if p == "*" || strings.EqualFold(p, "x") {
					wild = true
					continue
				}
				if wild {
					known = false
					break
				}
				if _, e := strconv.ParseUint(p, 10, 32); e != nil {
					known = false
					break
				}
				if len(p) > 1 && p[0] == '0' {
					known = false
					break
				}
				exact = append(exact, p)
			}
			if !known {
				continue
			}
			if len(exact) == 0 {
				if op != "" && op != "=" {
					known = false
				}
				continue
			}
			low := strings.Join(exact, ".")
			cmp, ok := compare(version, low)
			if !ok {
				known = false
				continue
			}
			yes := false
			switch op {
			case "", "=":
				if len(exact) == 3 {
					yes = cmp == 0
				} else {
					up, ok := increment(exact)
					if !ok {
						known = false
						continue
					}
					c, _ := compare(version, up)
					yes = cmp >= 0 && c < 0
				}
			case ">=":
				yes = cmp >= 0
			case "<":
				yes = cmp < 0
			case ">", "<=":
				if len(exact) < 3 {
					upper, ok := increment(exact)
					if !ok {
						known = false
						continue
					}
					c, _ := compare(version, upper)
					yes = c >= 0
					if op == "<=" {
						yes = c < 0
					}
					break
				}
				if op == ">" {
					yes = cmp > 0
				} else {
					yes = cmp <= 0
				}
			case "~", "^":
				count := len(exact)
				if op == "~" {
					if count > 1 {
						count = 2
					}
				} else {
					count = 1
					for count < len(exact) && exact[count-1] == "0" {
						count++
					}
				}
				up, ok := increment(exact[:count])
				if !ok {
					known = false
					continue
				}
				c, _ := compare(version, up)
				yes = cmp >= 0 && c < 0
			}
			matched = matched && yes
		}
		if known && matched {
			anyMatch = true
		}
		if !known {
			unknown = true
		}
	}
	if unknown {
		return false, false
	}
	return anyMatch, true
}
