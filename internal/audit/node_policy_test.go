package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeSelectorSubset(t *testing.T) {
	for _, tc := range []struct {
		selector, target string
		want             bool
	}{
		{"latest", "3.0.0", true}, {"*", "3.0.0", true}, {"", "3.0.0", true},
		{"1.0.0", "1.0.0", true}, {"1.0.0", "1.0.1", false},
		{"^1.2.3", "1.9.0", true}, {"^1.2.3", "2.0.0", false}, {"^1.2.3", "1.2.2", false},
		{"~1.2.3", "1.2.9", true}, {"~1.2.3", "1.3.0", false},
		{"^0.2.3", "0.2.9", true}, {"^0.2.3", "0.3.0", false}, {"^0.0.3", "0.0.4", false},
		{"^1", "1.9.0", false}, {"1.x", "1.9.0", false}, {">=1.0.0", "2.0.0", false}, {"next", "2.0.0", false},
		{"^1.2.3 || ^2.0.0", "2.0.0", false}, {"latest", "2.0.0-beta", false}, {"01.0.0", "1.0.0", false}, {"^1.0.0", "1.2.0+build", false},
	} {
		if got := NodeSelectorAllows(tc.selector, tc.target); got != tc.want {
			t.Errorf("%q allows %q = %v", tc.selector, tc.target, got)
		}
	}
}

func TestNpmResolvedVersionIsNotAUserPin(t *testing.T) {
	scope := t.TempDir()
	path := filepath.Join(scope, "lib/node_modules/example")
	if e := os.MkdirAll(path, 0755); e != nil {
		t.Fatal(e)
	}
	write := func(text string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(path, "package.json"), []byte(text), 0644); e != nil {
			t.Fatal(e)
		}
	}
	write(`{"name":"example","version":"1.0.0","_resolved":"https://registry.npmjs.org/example/-/example-1.0.0.tgz"}`)
	if e := os.WriteFile(filepath.Join(scope, "lib/node_modules/.package-lock.json"), []byte(`{"packages":{"node_modules/example":{"version":"1.0.0"}}}`), 0644); e != nil {
		t.Fatal(e)
	}
	policy, e := ReadNodeSelector("npm", "example", path, scope)
	if e != nil || policy.Source != "npm-default-latest" || policy.Selector != "latest" {
		t.Fatal(policy, e)
	}
	for _, record := range []string{`"_requested":{"rawSpec":"1.0.0"}`, `"_from":"example@1.0.0"`, `"_spec":"https://SECRET@private.invalid"`} {
		write(`{"name":"example","version":"1.0.0",` + record + `}`)
		p, err := ReadNodeSelector("npm", "example", path, scope)
		b, _ := json.Marshal(p)
		if err == nil || strings.Contains(string(b), "SECRET") {
			t.Fatal("opaque original request accepted or leaked", p, err)
		}
	}
}

func TestNodeGlobalManifestDigestAndAmbiguousMembership(t *testing.T) {
	scope := t.TempDir()
	path := filepath.Join(scope, "node_modules/example")
	write := func(s string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(scope, "package.json"), []byte(s), 0644); e != nil {
			t.Fatal(e)
		}
	}
	write(`{"dependencies":{"example":"^1.0.0"}}`)
	a, e := ReadNodeSelector("bun", "example", path, scope)
	if e != nil {
		t.Fatal(e)
	}
	write(`{"dependencies":{"example":"^1.0.0"},"description":"changed"}`)
	b, e := ReadNodeSelector("bun", "example", path, scope)
	if e != nil || a.Digest == b.Digest {
		t.Fatal("manifest drift unbound", e)
	}
	for _, s := range []string{`{}`, `{"dependencies":{"example":"^1.0.0"},"optionalDependencies":{"example":"^1.0.0"}}`, `{"dependencies":{"example":"file:../local"}}`, `{"dependencies":{"example":"npm:other@1.0.0"}}`, `{"dependencies":{"example":"1.0.0","example":"latest"}}`, `{"dependencies":{"example":"^1.0.0"},"pnpm":{"overrides":{"example":"2.0.0"}}}`} {
		write(s)
		if _, e := ReadNodeSelector("pnpm", "example", path, scope); e == nil {
			t.Fatal("ambiguous selector accepted", s)
		}
	}
}
