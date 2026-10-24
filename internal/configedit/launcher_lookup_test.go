package configedit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLauncherRejectsNonRegistryLookupLocations(t *testing.T) {
	for _, tc := range []struct{ name, path, data string }{
		{"local same version", "node_modules/@example/server/package.json", `{"name":"@example/server","version":"2.0.0","_resolved":"file:../private"}`},
		{"local alias", "node_modules/alias/package.json", `{"name":"@example/server","version":"2.0.0"}`},
		{"local project bin", "package.json", `{"name":"private-project","bin":{"@example/server":"private.js"}}`},
		{"workspace", "package.json", `{"workspaces":["../private"]}`},
		{"global same version", "global/lib/node_modules/@example/server/package.json", `{"name":"@example/server","version":"2.0.0","_resolved":"file:../private"}`},
		{"global alias", "global/lib/node_modules/alias/package.json", `{"name":"@example/server","version":"2.0.0","_resolved":"https://private.invalid/pkg.tgz"}`},
		{"global literal bin", "global/bin/@example/server@2.0.0", "private executable"},
		{"global bare bin", "global/bin/@example/server", "private executable"},
		{"execution cache", ".npm/_npx/fixture/node_modules/@example/server/package.json", `{"name":"@example/server","version":"2.0.0","_resolved":"file:../private"}`},
		{"hidden global lock", "global/lib/node_modules/.package-lock.json", `{"packages":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, _, home := launcherSourceFixture(t)
			if _, err := NPMLauncherSource(item); err != nil {
				t.Fatal("clean source", err)
			}
			fixtureFile(t, filepath.Join(home, tc.path), tc.data)
			if _, err := NPMLauncherSource(item); err != errNPMLookup {
				t.Fatal("unverified lookup accepted", err)
			}
		})
	}
}

func TestLauncherLookupBindsAncestorsAndGlobalAliases(t *testing.T) {
	item, path, home := launcherSourceFixture(t)
	child := filepath.Join(home, "project", "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	fixtureFile(t, path, strings.Replace(string(data), fmt.Sprintf("cwd = %q", home), fmt.Sprintf("cwd = %q", child), 1))
	alias := filepath.Join(home, "global/lib/node_modules/alias/package.json")
	fixtureFile(t, alias, `{"name":"unrelated-package","version":"1.0.0"}`)
	first, err := NPMLauncherSource(item)
	if err != nil {
		t.Fatal("unrelated global package should remain usable", err)
	}
	fixtureFile(t, alias, `{"name":"unrelated-package","version":"1.1.0"}`)
	next, err := NPMLauncherSource(item)
	if err != nil || first.Identity == next.Identity {
		t.Fatal("global lookup not bound", err)
	}
	fixtureFile(t, alias, `{"name":"@example/server","version":"2.0.0"}`)
	if _, err := NPMLauncherSource(item); err == nil {
		t.Fatal("changed global alias accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(alias)); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(home, "project/node_modules/@example/server/package.json"), `{"name":"@example/server","version":"2.0.0"}`)
	if _, err := NPMLauncherSource(item); err == nil {
		t.Fatal("ancestor package accepted")
	}
}

func TestLauncherSourceRecheckedAfterBackupBeforeCommit(t *testing.T) {
	item, path, home := launcherSourceFixture(t)
	data, _ := os.ReadFile(path)
	// A bounded large comment gives the watcher a real backup/staging window;
	// no test hook or artificial delay is added to the production commit path.
	original := append(data, []byte("#"+strings.Repeat("x", 2<<20)+"\n")...)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NPMLauncherSource(item)
	if err != nil {
		t.Fatal(err)
	}
	target, err := PlanReference(item, "@example/server@2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	target.LauncherSourceIdentity = source.Identity
	changed := make(chan error, 1)
	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		for {
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				changed <- err
				return
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "."+filepath.Base(path)+".keepupdated-backup-") {
					changed <- os.WriteFile(filepath.Join(home, ".npmrc"), []byte("registry=https://private.invalid/\n"), 0600)
					return
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Microsecond):
			}
		}
	}()
	defer close(stop)
	<-started
	result, applyErr := Apply(target)
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backup mutation was not observed")
	}
	if applyErr == nil || result.Changed || result.Reason != "mcp_launcher_source_changed" {
		t.Fatal("post-backup source drift committed", result, applyErr)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(original) {
		t.Fatal("original was changed", err)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil || string(backup) != string(original) {
		t.Fatal("backup was not retained", err)
	}
}

func TestLauncherLookupBindsManifestLinksAndEmptyCache(t *testing.T) {
	item, _, home := launcherSourceFixture(t)
	manifest := filepath.Join(home, "package-metadata/unrelated.json")
	fixtureFile(t, manifest, `{"name":"unrelated","version":"1.0.0"}`)
	link := filepath.Join(home, "global/lib/node_modules/alias/package.json")
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manifest, link); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".npm/_npx"), 0700); err != nil {
		t.Fatal(err)
	}
	first, err := NPMLauncherSource(item)
	if err != nil {
		t.Fatal("clean cache/unrelated global manifest link", err)
	}
	other := filepath.Join(home, "package-metadata/replacement.json")
	fixtureFile(t, other, `{"name":"unrelated","version":"1.0.0"}`)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	second, err := NPMLauncherSource(item)
	if err != nil || first.Identity == second.Identity {
		t.Fatal("global manifest link not bound", err)
	}
	fixtureFile(t, other, `{"name":"@example/server","version":"2.0.0"}`)
	if _, err := NPMLauncherSource(item); err == nil {
		t.Fatal("global manifest replacement shadow accepted")
	}
}

func TestLauncherDoesNotGuessGlobalPrefixAndRejectsUnscopedBin(t *testing.T) {
	item, path, home := launcherSourceFixture(t)
	data, _ := os.ReadFile(path)
	fixtureFile(t, path, strings.ReplaceAll(string(data), "@example/server", "example-server"))
	if _, err := NPMLauncherSource(item); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(home, "global/bin/example-server@2.0.0"), "do not execute")
	if _, err := NPMLauncherSource(item); err == nil {
		t.Fatal("literal versioned global bin accepted")
	}
	if err := os.Remove(filepath.Join(home, "global/bin/example-server@2.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "npm/npmrc")); err != nil {
		t.Fatal(err)
	}
	if _, err := NPMLauncherSource(item); err == nil {
		t.Fatal("global prefix inferred from config directory")
	}
}
