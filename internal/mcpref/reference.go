// Package mcpref recognizes bounded package launcher references without executing them.
package mcpref

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

type Reference struct {
	Launcher string `json:"launcher"`
	Package  string `json:"package"`
	Value    string `json:"value"`
	Version  string `json:"version,omitempty"`
	Index    int    `json:"-"`
	Prefix   string `json:"-"`
}

var npmName = regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9][a-z0-9._-]*$`)
var pythonName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var npmRelease = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?(?:\+[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?$`)

// uv tries PythonRequest before package parsing; reserve interpreter-looking forms, including Windows pythonw.
var uvInterpreter = regexp.MustCompile(`(?i)^(?:pythonw?|cpython|pypy|graalpy|pyodide)(?:$|[@0-9<>=!~+]|v[0-9])`)
var release = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*(?:[ab]|rc|post|dev)?[0-9]*(?:-[A-Za-z0-9.-]+)?$`)

func Parse(command string, args []string) (Reference, error) {
	r := Reference{Launcher: filepath.Base(command), Index: -1}
	if r.Launcher == "bunx" {
		if command != "bunx" && (!filepath.IsAbs(command) || filepath.Clean(command) != command) || strings.ContainsAny(command, "\x00\n\r\t$`|;&<>") {
			return r, errors.New("unsupported_launcher")
		}
		if len(args) == 0 {
			return r, errors.New("missing_package_reference")
		}
		// bunx flags are supported only after the package, where Bun passes them to the server.
		if strings.HasPrefix(args[0], "-") {
			return r, errors.New("unsupported_launcher_option")
		}
		r.Index, r.Value = 0, args[0]
		name, v, ok := split(r.Launcher, r.Value)
		if !ok {
			return r, errors.New("unsupported_package_reference")
		}
		r.Package, r.Version = name, v
		return r, nil
	}
	start := 0
	if r.Launcher == "uv" {
		if len(args) < 2 || args[0] != "tool" || args[1] != "run" {
			return r, errors.New("unsupported_launcher")
		}
		start = 2
	}
	if r.Launcher != "npx" && r.Launcher != "uvx" && r.Launcher != "uv" {
		return r, errors.New("unsupported_launcher")
	}
	seenYes := false
	for n := start; n < len(args); n++ {
		arg := args[n]
		if r.Launcher == "npx" && (arg == "-y" || arg == "--yes") {
			if seenYes {
				return r, errors.New("duplicate_launcher_option")
			}
			seenYes = true
			continue
		}
		flag := "--from"
		if r.Launcher == "npx" {
			flag = "--package"
		}
		if arg == flag || r.Launcher == "npx" && arg == "-p" {
			if n+2 >= len(args) {
				return r, errors.New("missing_package_or_executable")
			}
			n++
			r.Index = n
			r.Value = args[n]
			if !pythonName.MatchString(args[n+1]) {
				return r, errors.New("ambiguous_launcher_executable")
			}
		} else if strings.HasPrefix(arg, flag+"=") {
			if n+1 >= len(args) || !pythonName.MatchString(args[n+1]) {
				return r, errors.New("missing_package_or_executable")
			}
			r.Index = n
			r.Prefix = flag + "="
			r.Value = strings.TrimPrefix(arg, r.Prefix)
		} else {
			if strings.HasPrefix(arg, "-") {
				return r, errors.New("unsupported_launcher_option")
			}
			r.Index = n
			r.Value = arg
		}
		name, v, ok := split(r.Launcher, r.Value)
		if !ok {
			return r, errors.New("unsupported_package_reference")
		}
		r.Package, r.Version = name, v
		return r, nil // Remaining arguments belong to the server and are never inspected or reported.
	}
	return r, errors.New("missing_package_reference")
}

func split(launcher, value string) (string, string, bool) {
	name, v := value, ""
	if launcher == "npx" || launcher == "bunx" {
		if n := strings.LastIndex(value, "@"); n > 0 {
			name, v = value[:n], value[n+1:]
			if v == "" {
				return "", "", false
			}
		}
		// Bun remaps tsc/claude; bare add/exec can dispatch before package parsing.
		if launcher == "bunx" && (name == "tsc" || name == "claude" || value == "add" || value == "exec") {
			return "", "", false
		}
		return name, v, npmName.MatchString(name) && (v == "" || v == "latest" || npmRelease.MatchString(v))
	}
	if uvInterpreter.MatchString(value) {
		return "", "", false
	}
	if n := strings.Index(value, "=="); n > 0 {
		name, v = value[:n], value[n+2:]
		if v == "" {
			return "", "", false
		}
	} else if n := strings.Index(value, "@"); n > 0 {
		name, v = value[:n], value[n+1:]
		if v == "" {
			return "", "", false
		}
	}
	return name, v, pythonName.MatchString(name) && (v == "" || v == "latest" || release.MatchString(v))
}

func (r Reference) Replacement(value string) (string, error) {
	name, v, ok := split(r.Launcher, value)
	if !ok || name != r.Package || v == "" || v == "latest" || value == r.Value {
		return "", errors.New("replacement_requires_same_package_and_exact_version")
	}
	return r.Prefix + value, nil
}
