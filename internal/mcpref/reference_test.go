package mcpref

import (
	"path/filepath"
	"testing"
)

func TestLauncherReferences(t *testing.T) {
	for _, tc := range []struct {
		command       string
		args          []string
		value, target string
		index         int
	}{
		{"/tools/npx", []string{"-y", "@scope/server@1.2.3", "--token", "SECRET"}, "@scope/server@1.2.3", "@scope/server@2.0.0", 1},
		{"npx", []string{"--package=server@1.2.3", "serve", "--port", "9000"}, "server@1.2.3", "server@2.0.0", 0},
		{"npx", []string{"--yes", "-p", "server@latest", "serve"}, "server@latest", "server@2.0.0", 2},
		{"bunx", []string{"server"}, "server", "server@2.0.0", 0},
		{"bunx", []string{"server@latest"}, "server@latest", "server@2.0.0", 0},
		{"bunx", []string{"server@1.2.3"}, "server@1.2.3", "server@2.0.0", 0},
		{"bunx", []string{"@scope/server"}, "@scope/server", "@scope/server@2.0.0", 0},
		{"bunx", []string{"@scope/server@latest"}, "@scope/server@latest", "@scope/server@2.0.0", 0},
		{"/tools/bunx", []string{"@scope/server@1.2.3-beta.1+build.2", "--token", "SECRET"}, "@scope/server@1.2.3-beta.1+build.2", "@scope/server@2.0.0", 0},
		{"/tools with spaces/bunx", []string{"server@1.2.3", "--bun", "--package", "other", "-p", "other", "--install", "--no-install", "server@1.2.3"}, "server@1.2.3", "server@2.0.0", 0},
		{"bunx", []string{"@scope/tsc@1.2.3"}, "@scope/tsc@1.2.3", "@scope/tsc@2.0.0", 0},
		{"bunx", []string{"@scope/claude@1.2.3"}, "@scope/claude@1.2.3", "@scope/claude@2.0.0", 0},
		{"bunx", []string{"add@1.2.3"}, "add@1.2.3", "add@2.0.0", 0},
		{"bunx", []string{"exec@latest"}, "exec@latest", "exec@2.0.0", 0},
		{"uvx", []string{"--from", "server==1.2.3", "serve", "--secret", "SECRET"}, "server==1.2.3", "server==2.0.0", 1},
		{"uvx", []string{"--from=server==1.2.3", "serve"}, "server==1.2.3", "server==2.0.0", 0},
		{"uvx", []string{"server"}, "server", "server==2.0.0", 0},
		{"uv", []string{"tool", "run", "server@1.2.3"}, "server@1.2.3", "server@2.0.0", 2},
	} {
		t.Run(tc.command+tc.value, func(t *testing.T) {
			r, err := Parse(tc.command, tc.args)
			if err != nil || r.Value != tc.value || r.Index != tc.index || r.Launcher != filepath.Base(tc.command) {
				t.Fatal(r, err)
			}
			replacement, err := r.Replacement(tc.target)
			if err != nil || replacement != r.Prefix+tc.target {
				t.Fatal(replacement, err)
			}
			for _, bad := range []string{r.Value, "other@2.0.0", "server@latest", "server", "https://example.test/pkg", "server@2.0.0;run", r.Package + "@latest", r.Package + "@^2.0.0", r.Package + "@~2.0.0", r.Package + "@"} {
				if _, err := r.Replacement(bad); err == nil {
					t.Fatal("accepted replacement", bad)
				}
			}
		})
	}
}

func TestRejectAmbiguousAndSourceChangingLaunchers(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"bunx", nil},
		{"./bunx", []string{"server@1.0.0"}},
		{"tools/bunx", []string{"server@1.0.0"}},
		{"/tools/../bunx", []string{"server@1.0.0"}},
		{"/tools//bunx", []string{"server@1.0.0"}},
		{"/tools/bunx/", []string{"server@1.0.0"}},
		{"/$TOOLS/bunx", []string{"server@1.0.0"}},
		{"/tools\n/bunx", []string{"server@1.0.0"}},
		{"/tools\u0000/bunx", []string{"server@1.0.0"}},
		{"bun", []string{"x", "server"}},
		{"/tools/bun", []string{"x", "server"}},
		{"env", []string{"bunx", "server"}},
		{"sh", []string{"-c", "bunx server"}},
		{"bunx", []string{""}},
		{"bunx", []string{"tsc"}},
		{"bunx", []string{"tsc@latest"}},
		{"bunx", []string{"tsc@1.2.3"}},
		{"bunx", []string{"claude"}},
		{"bunx", []string{"claude@latest"}},
		{"bunx", []string{"claude@1.2.3"}},
		{"bunx", []string{"add"}},
		{"bunx", []string{"exec"}},
		{"bunx", []string{"server@^1.0.0"}},
		{"bunx", []string{"server@~1.0.0"}},
		{"bunx", []string{"server@1"}},
		{"bunx", []string{"server@next"}},
		{"bunx", []string{"server@"}},
		{"bunx", []string{"@scope/server@"}},
		{"bunx", []string{"https://example.test/pkg"}},
		{"bunx", []string{"git+https://example.test/pkg"}},
		{"bunx", []string{"github:user/pkg"}},
		{"bunx", []string{"file:./pkg"}},
		{"bunx", []string{"./pkg"}},
		{"bunx", []string{"/tmp/pkg"}},
		{"bunx", []string{"user/pkg"}},
		{"bunx", []string{"server==1.0.0"}},
		{"bunx", []string{"${PACKAGE}"}},
		{"bunx", []string{"--bun", "server"}},
		{"bunx", []string{"--package", "server", "serve"}},
		{"bunx", []string{"-p", "server", "serve"}},
		{"bunx", []string{"--package=server", "serve"}},
		{"bunx", []string{"--package", "server", "--package", "other", "serve"}},
		{"bunx", []string{"-p", "server", "-p", "other", "serve"}},
		{"bunx", []string{"--package=server", "--package=other", "serve"}},
		{"bunx", []string{"--registry", "https://example.test", "server"}},
		{"bunx", []string{"--install", "server"}},
		{"bunx", []string{"--cache-dir", "/tmp/cache", "server"}},
		{"bunx", []string{"--unknown", "server"}},
		{"bunx", []string{"--", "server"}},
		{"bunx", []string{"-y", "server"}},
		{"npm", []string{"exec", "server"}}, {"sh", []string{"-c", "npx server"}}, {"uv", []string{"run", "server"}},
		{"npx", nil}, {"npx", []string{"-y", "--yes", "server"}},
		{"npx", []string{"--package", "server", "--package", "other", "serve"}},
		{"npx", []string{"--package=server", "--package=other", "serve"}},
		{"npx", []string{"--registry", "https://example.test", "server"}},
		{"npx", []string{"-c", "server"}}, {"npx", []string{"--package", "server"}},
		{"npx", []string{"git+https://example.test/server"}}, {"npx", []string{"./local"}},
		{"npx", []string{"server@^1.0.0"}}, {"npx", []string{"server@"}},
		{"uvx", []string{"--from", "server", "--from", "other", "serve"}},
		{"uvx", []string{"--with", "other", "server"}}, {"uvx", []string{"server>=1.0"}},
		{"uvx", []string{"server[extra]"}}, {"uvx", []string{"server @ https://example.test"}},
	} {
		if r, err := Parse(tc.command, tc.args); err == nil {
			t.Fatal("ambiguous reference", r, tc)
		}
	}
}

func TestUVInterpreterSelectorsAreNotPackageReferences(t *testing.T) {
	// uv's ToolRequest tries PythonRequest on --from, or on the command when --from is absent.
	for _, selector := range []string{
		"python", "python@3.12", "python3.12", "python312", "python3.13t", "pythonv3.12", "PYTHON@3.12",
		"cpython", "cpython@3.12", "pypy", "pypy39", "pypy@3.10", "graalpy@3.11", "pyodide@3.12",
		"python==3.12", "python@latest", "pythonw", "pythonw3.12",
	} {
		for _, launcher := range []string{"uvx", "uv"} {
			for _, args := range [][]string{{selector, "-m", "server"}, {"--from", selector, "server"}, {"--from=" + selector, "server"}} {
				if launcher == "uv" {
					args = append([]string{"tool", "run"}, args...)
				}
				if ref, err := Parse(launcher, args); err == nil {
					t.Errorf("interpreter accepted as package: %s %v: %+v", launcher, args, ref)
				}
			}
		}
	}
	for _, launcher := range []string{"uvx", "uv"} {
		ref := Reference{Launcher: launcher, Package: "python", Value: "python@3.12"}
		if _, err := ref.Replacement("python@3.13"); err == nil {
			t.Fatal("interpreter replacement accepted", launcher)
		}
	}
}

func TestInterpreterGuardPreservesPackageNames(t *testing.T) {
	for _, name := range []string{"python", "python3.12", "cpython", "pypy", "graalpy", "pyodide", "pythonw"} {
		for _, args := range [][]string{{name + "@1.0.0"}, {"--package", name + "@1.0.0", "server"}} {
			ref, err := Parse("npx", args)
			if err != nil {
				t.Fatal("npm package rejected", name, err)
			}
			if _, err := ref.Replacement(name + "@2.0.0"); err != nil {
				t.Fatal("npm replacement rejected", name, err)
			}
		}
	}
	// uv's tool-name parser does not use bare versions or short implementation aliases.
	for _, name := range []string{"python-lsp-server", "python_mcp", "pythonic", "cpython-tools", "pypy-server", "graalpy-mcp", "pyodide-mcp", "cp312", "pp39", "gp311", "39"} {
		for _, launcher := range []string{"uvx", "uv"} {
			args := []string{"--from", name + "==1.0.0", "python"}
			if launcher == "uv" {
				args = append([]string{"tool", "run"}, args...)
			}
			ref, err := Parse(launcher, args)
			if err != nil {
				t.Fatal("ordinary package rejected", name, err)
			}
			if _, err := ref.Replacement(name + "==2.0.0"); err != nil {
				t.Fatal("ordinary replacement rejected", name, err)
			}
		}
	}
}
