package audit

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func provenancePackage(t *testing.T, s *Scanner, name string, resolved any) string {
	t.Helper()
	path := filepath.Join(s.Config.Home, "global/node_modules", filepath.FromSlash(name))
	m := map[string]any{"name": name, "version": "1.2.3"}
	if resolved != nil {
		m["_resolved"] = resolved
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "package.json"), string(body))
	return path
}

func TestNpmPublicResolvedRequiresExactGlobalIdentity(t *testing.T) {
	for _, name := range []string{"example", "@scope/example"} {
		for _, owner := range []string{"npm", "pnpm", "bun"} {
			t.Run(name+"/"+owner, func(t *testing.T) {
				s := fixture(t)
				path := provenancePackage(t, s, name, "https://registry.npmjs.org/"+name+"/-/example-1.2.3.tgz")
				s.nodePackage(path, owner)
				want := "npm-public-candidate"
				if owner == "npm" {
					want = "npm-public"
				}
				if len(s.Report.Items) != 1 || s.Report.Items[0].Source != want {
					t.Fatal(s.Report.Items)
				}
			})
		}
	}
}

func TestNpmResolvedPrivateOrAmbiguousURLsRemainCandidates(t *testing.T) {
	for _, recorded := range []any{
		"https://user:SECRET@registry.npmjs.org/example/-/example-1.2.3.tgz",
		"https://registry.npmjs.org/example/-/example-1.2.3.tgz?token=SECRET",
		"https://registry.npmjs.org/example/-/example-1.2.3.tgz?",
		"https://registry.npmjs.org/example/-/example-1.2.3.tgz#SECRET",
		"https://registry.npmjs.org:443/example/-/example-1.2.3.tgz",
		"https://registry.npmjs.org.evil.invalid/example/-/example-1.2.3.tgz",
		"https://registry.private.invalid/example/-/example-1.2.3.tgz",
		"https://registry.npmjs.org/example/-/example-9.9.9.tgz",
		"https://registry.npmjs.org/other/-/other-1.2.3.tgz",
		"https://registry.npmjs.org/%65xample/-/example-1.2.3.tgz",
		"http://registry.npmjs.org/example/-/example-1.2.3.tgz",
		map[string]any{"token": "SECRET"},
	} {
		s := fixture(t)
		path := provenancePackage(t, s, "example", recorded)
		s.nodePackage(path, "npm")
		if len(s.Report.Items) != 1 || s.Report.Items[0].Source != "npm-public-candidate" {
			t.Fatal("unsupported provenance accepted", s.Report.Items)
		}
		b, _ := json.Marshal(s.Report)
		if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "registry.private.invalid") {
			t.Fatal("private provenance data escaped")
		}
	}
}

func TestNpmHiddenLockRequiresMatchingFreshEntry(t *testing.T) {
	for _, c := range []struct {
		name, key, entryName, version, resolved string
		link, stale                             bool
		source                                  string
	}{
		{"example", "node_modules/example", "", "1.2.3", "https://registry.npmjs.org/example/-/example-1.2.3.tgz", false, false, "npm-public"},
		{"@scope/example", "node_modules/@scope/example", "@scope/example", "1.2.3", "https://registry.npmjs.org/@scope/example/-/example-1.2.3.tgz", false, false, "npm-public"},
		{"example", "example", "example", "1.2.3", "https://registry.npmjs.org/example/-/example-1.2.3.tgz", false, false, "npm-public-candidate"},
		{"example", "node_modules/example", "other", "1.2.3", "https://registry.npmjs.org/example/-/example-1.2.3.tgz", false, false, "npm-public-candidate"},
		{"example", "node_modules/example", "example", "9.9.9", "https://registry.npmjs.org/example/-/example-1.2.3.tgz", false, false, "npm-public-candidate"},
		{"example", "node_modules/example", "example", "1.2.3", "https://registry.npmjs.org/example/-/example-1.2.3.tgz", true, false, "npm-public-candidate"},
		{"example", "node_modules/example", "example", "1.2.3", "https://registry.npmjs.org/example/-/example-1.2.3.tgz", false, true, "npm-public-candidate"},
		{"example", "node_modules/example", "example", "1.2.3", "https://registry.private.invalid/example/-/example-1.2.3.tgz", false, false, "npm-public-candidate"},
	} {
		s := fixture(t)
		path := provenancePackage(t, s, c.name, nil)
		root := filepath.Dir(path)
		if strings.HasPrefix(c.name, "@") {
			root = filepath.Dir(root)
		}
		lockPath := filepath.Join(root, ".package-lock.json")
		body, err := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{c.key: map[string]any{"name": c.entryName, "version": c.version, "resolved": c.resolved, "link": c.link}}})
		if err != nil {
			t.Fatal(err)
		}
		write(t, lockPath, string(body))
		if c.stale {
			if err := os.Chtimes(lockPath, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
				t.Fatal(err)
			}
		}
		s.nodePackage(path, "npm")
		if len(s.Report.Items) != 1 || s.Report.Items[0].Source != c.source {
			t.Fatal(c, s.Report.Items)
		}
	}
}

func TestNpmLinksAndWrongInstallPathsCannotBorrowProvenance(t *testing.T) {
	for _, kind := range []string{"package-link", "metadata-link", "lock-link", "name-path-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			s := fixture(t)
			path := provenancePackage(t, s, "example", nil)
			root := filepath.Dir(path)
			public := `{"name":"example","version":"1.2.3","_resolved":"https://registry.npmjs.org/example/-/example-1.2.3.tgz"}`
			switch kind {
			case "package-link":
				outside := filepath.Join(s.Config.Home, "linked")
				write(t, filepath.Join(outside, "package.json"), public)
				if err := os.Remove(filepath.Join(path, "package.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				link(t, outside, path)
			case "metadata-link":
				outside := filepath.Join(s.Config.Home, "metadata.json")
				write(t, outside, public)
				if err := os.Remove(filepath.Join(path, "package.json")); err != nil {
					t.Fatal(err)
				}
				link(t, outside, filepath.Join(path, "package.json"))
			case "lock-link":
				outside := filepath.Join(s.Config.Home, "metadata.json")
				write(t, outside, `{"lockfileVersion":3,"packages":{"node_modules/example":{"version":"1.2.3","resolved":"https://registry.npmjs.org/example/-/example-1.2.3.tgz"}}}`)
				link(t, outside, filepath.Join(root, ".package-lock.json"))
			case "name-path-mismatch":
				path = filepath.Join(root, "another")
				write(t, filepath.Join(path, "package.json"), public)
			}
			s.nodePackage(path, "npm")
			if len(s.Report.Items) != 1 || s.Report.Items[0].Source != "npm-public-candidate" {
				t.Fatal(s.Report.Items)
			}
		})
	}
}

func TestConfirmedPublicNpmUsesSourceComparison(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"name":"example","version":"1.0.0"}`), nil
	})
	i := item("node-package", "example", "fixture", "npm")
	i.Source = "npm-public"
	i.Identity = "example"
	i.Version = "1.0.0"
	i.lookup = "example"
	s.checkItem(&i)
	if i.Update.Status != "current" {
		t.Fatal(i)
	}
	i.Source = "npm-public-candidate"
	s.checkItem(&i)
	if i.Update.Status != "unknown" {
		t.Fatal(i)
	}
}
