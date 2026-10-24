package manage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type nativeTransport func(*http.Request) (*http.Response, error)

func (f nativeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func uvArchive(t *testing.T, version string) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"uv", "uvx"} {
		content := []byte(name + " " + version)
		if err := tw.WriteHeader(&tar.Header{Name: "uv-aarch64-apple-darwin/" + name, Mode: 0755, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func uvReceipt(t *testing.T, home, version string) string {
	t.Helper()
	r := audit.UVReceipt{Binaries: []string{"uv", "uvx"}, BinaryAliases: map[string][]string{}, CDylibs: []string{}, CStaticlibs: []string{}, InstallLayout: "flat", InstallPrefix: filepath.Join(home, ".local/bin"), Version: version}
	r.Provider.Source = "cargo-dist"
	r.Provider.Version = "0.32.0"
	r.Source.AppName = "uv"
	r.Source.Name = "uv"
	r.Source.Owner = "astral-sh"
	r.Source.ReleaseType = "github"
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func nativeCLIFixture(t *testing.T) (*fixture, audit.Item) {
	f := setup(t)
	for _, name := range []string{"uv", "uvx"} {
		f.write(filepath.Join(f.home, ".local/bin", name), name+" 0.13.0", 0755)
	}
	f.write(audit.UVReceiptPath(f.home), uvReceipt(t, f.home, "0.13.0"), 0644)
	i := audit.Item{ID: "native-uv", Kind: "executable", Name: "uv", Identity: "uv", Owner: "uv-standalone", Source: "github:astral-sh/uv", Channel: "stable", Path: filepath.Join(f.home, ".local/bin/uv"), Version: "0.13.0", ActiveVersion: "0.13.0", Active: true, ActivityKnown: true, Architecture: []string{"arm64"}, Health: audit.Result{Status: "ok"}}
	f.r.Items = []audit.Item{i, {ID: "tool", Kind: "python-package", Name: "example", Owner: "uv", Version: "1.0.0", Path: filepath.Join(f.home, ".local/share/uv/tools/example")}}
	archives := map[string][]byte{"0.13.0": uvArchive(t, "0.13.0"), "0.13.1": uvArchive(t, "0.13.1")}
	f.o.NativeCLIHTTPClient = &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		var data []byte
		if r.URL.String() == "https://api.github.com/repos/astral-sh/uv/releases/latest" {
			data = []byte(`{"tag_name":"0.13.1","draft":false,"prerelease":false}`)
		} else {
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) != 7 || r.URL.Host != "github.com" {
				t.Fatalf("unexpected request: %s", r.URL)
			}
			data = archives[parts[5]]
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				data = []byte(hashText(string(data)) + "  uv-aarch64-apple-darwin.tar.gz\n")
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	var err error
	f.o.ResolvedTargets, err = ResolveNativeCLITargets(context.Background(), f.r, Selection{Action: "update", IDs: []string{i.ID}}, f.o, nil)
	if err != nil || !f.o.ResolvedTargets[i.ID].OriginVerified {
		t.Fatal(f.o.ResolvedTargets, err)
	}
	return f, i
}

func nativeSelection(id string) Selection {
	return Selection{Action: "update", IDs: []string{id}, Special: true, Consents: []string{"runtime_update"}}
}

func TestNativeCLISeparateConsentScopeAndExactCommand(t *testing.T) {
	f, i := nativeCLIFixture(t)
	for _, s := range []Selection{{Action: "update", All: true, Special: true, Consents: []string{"runtime_update"}}, {Action: "update", IDs: []string{i.ID}, Special: true}, {Action: "update", IDs: []string{i.ID}, Consents: []string{"runtime_update"}}, {Action: "uninstall", IDs: []string{i.ID}, Special: true, Consents: []string{"runtime_update"}}} {
		p := f.plan(s)
		for _, item := range p.Items {
			if item.ID == i.ID && item.Status == "ready" {
				t.Fatal(s, item)
			}
		}
	}
	p := f.plan(nativeSelection(i.ID))
	pi := p.Items[0]
	if pi.Status != "ready" || pi.RuntimeReview == nil || len(pi.RuntimeReview.Consumers) != 1 || !pi.RequiresScripts || len(pi.AffectedPaths) != 3 {
		t.Fatal(pi)
	}
	command, err := commandFor(pi, "update", f.o, f.state)
	if err != nil || command.Path != i.Path || strings.Join(command.Args, " ") != "self update 0.13.1" || !contains(command.Env, "UV_NO_MODIFY_PATH=1") || !contains(command.Env, "XDG_CONFIG_HOME="+filepath.Join(f.home, ".config")) {
		t.Fatal(command, err)
	}
}

func TestNativeCLIUpdateVerificationAndRecovery(t *testing.T) {
	for _, mode := range []string{"success", "failure", "partial-known", "partial-unknown", "false-success"} {
		t.Run(mode, func(t *testing.T) {
			f, i := nativeCLIFixture(t)
			p := f.plan(nativeSelection(i.ID))
			out, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(_ context.Context, c Command) ([]byte, error) {
				if mode == "success" || mode == "partial-known" {
					for _, name := range []string{"uv", "uvx"} {
						f.write(filepath.Join(f.home, ".local/bin", name), name+" 0.13.1", 0755)
					}
					f.write(audit.UVReceiptPath(f.home), uvReceipt(t, f.home, "0.13.1"), 0644)
				}
				if mode == "success" {
					f.r.Items[0].Version = "0.13.1"
					f.r.Items[0].ActiveVersion = "0.13.1"
					return nil, nil
				}
				if mode == "partial-unknown" {
					f.write(i.Path, "unexpected content", 0755)
				}
				if mode == "false-success" {
					return nil, nil
				}
				return nil, errors.New("controlled failure")
			})))
			if err != nil || len(out.Results) != 1 {
				t.Fatal(out, err)
			}
			result := out.Results[0]
			if result.BackupPath == "" {
				t.Fatal("backup missing", result)
			}
			backup, err := os.ReadFile(filepath.Join(result.BackupPath, "uv"))
			if err != nil || string(backup) != "uv 0.13.0" {
				t.Fatal("backup wrong", err)
			}
			if mode == "success" {
				if result.Status != "updated" || !result.Changed {
					t.Fatal(result)
				}
				return
			}
			if mode == "false-success" && !result.CommandSucceeded {
				t.Fatal("command and verification conflated", result)
			}
			if result.Status != "failed" {
				t.Fatal("false success", result)
			}
			current, _ := os.ReadFile(i.Path)
			if mode == "partial-unknown" {
				if result.Reason != "native_cli_recovery_requires_review" || string(current) != "unexpected content" {
					t.Fatal("unexpected files overwritten", result)
				}
			} else if result.Reason != "native_cli_failed_original_restored" || string(current) != "uv 0.13.0" || result.Changed {
				t.Fatal("original not restored", result)
			}
		})
	}
}

func TestNativeCLIDriftBlocksBeforeAnyCommand(t *testing.T) {
	for _, mode := range []string{"binary", "receipt-source", "pin", "consumer", "non-current", "candidate"} {
		t.Run(mode, func(t *testing.T) {
			f, i := nativeCLIFixture(t)
			p := f.plan(nativeSelection(i.ID))
			executed := false
			switch mode {
			case "binary":
				f.write(i.Path, "locally modified", 0755)
			case "receipt-source":
				f.write(audit.UVReceiptPath(f.home), strings.Replace(uvReceipt(t, f.home, "0.13.0"), "astral-sh", "another-owner", 1), 0644)
			case "pin":
				f.write(audit.UVReceiptPath(f.home), strings.TrimSuffix(uvReceipt(t, f.home, "0.13.0"), "}")+`,"pinned":true}`, 0644)
			case "consumer":
				f.r.Items[1].Version = "2.0.0"
			case "non-current":
				f.r.Items[0].Active = false
			case "candidate":
				target := f.o.ResolvedTargets[i.ID]
				target.NativeCLI.TargetFiles["uv"] = strings.Repeat("a", 64)
				f.o.ResolvedTargets[i.ID] = target
			}
			// JSON decoding breaks shared pointers exactly as a persisted plan does.
			if mode == "candidate" {
				p.Items[0].Binding.ResolvedTarget.NativeCLI.TargetFiles["uv"] = hashText("uv 0.13.1")
				data, _ := json.Marshal(p)
				json.Unmarshal(data, &p)
				target := f.o.ResolvedTargets[i.ID]
				target.NativeCLI.TargetFiles["uv"] = strings.Repeat("a", 64)
				f.o.ResolvedTargets[i.ID] = target
			}
			_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { executed = true; return nil, nil })))
			if err == nil || executed {
				t.Fatal("drift executed", err)
			}
		})
	}
}

func TestNativeCLIResolverRejectsModifiedInstalledArtifact(t *testing.T) {
	f, i := nativeCLIFixture(t)
	f.write(i.Path, "modified before preview", 0755)
	targets, err := ResolveNativeCLITargets(context.Background(), f.r, nativeSelection(i.ID), f.o, nil)
	if err != nil || targets[i.ID].OriginVerified || targets[i.ID].Reason != "native_cli_local_modifications_or_permissions" {
		t.Fatal(targets, err)
	}
}

func TestNativeCLIReleaseChecksumRejectsTampering(t *testing.T) {
	f, i := nativeCLIFixture(t)
	original := f.o.NativeCLIHTTPClient.Transport
	f.o.NativeCLIHTTPClient = &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		response, err := original.RoundTrip(r)
		if err == nil && strings.HasSuffix(r.URL.Path, ".tar.gz") {
			response.Body.Close()
			response.Body = io.NopCloser(strings.NewReader("tampered archive"))
		}
		return response, err
	})}
	targets, err := ResolveNativeCLITargets(context.Background(), f.r, nativeSelection(i.ID), f.o, nil)
	if err != nil || targets[i.ID].OriginVerified {
		t.Fatal("tampered archive accepted", targets, err)
	}
}

func TestNativeCLIBackupChecksCopiedAndPersistedBytes(t *testing.T) {
	for _, mode := range []string{"copied-aba", "persisted-corruption"} {
		t.Run(mode, func(t *testing.T) {
			f, i := nativeCLIFixture(t)
			p := f.plan(nativeSelection(i.ID))
			if err := os.MkdirAll(f.state, 0700); err != nil {
				t.Fatal(err)
			}
			ops := nativeCLIOps{read: readNativeFile, writeBackup: writeNativeBackup, rename: os.Rename, link: os.Link}
			if mode == "copied-aba" {
				ops.read = func(path string) ([]byte, os.FileMode, error) {
					data, permissions, err := readNativeFile(path)
					if path == i.Path {
						data = []byte("different bytes observed during concurrent edit")
					}
					return data, permissions, err
				}
			}
			if mode == "persisted-corruption" {
				ops.writeBackup = func(path string, data []byte) error {
					if err := writeNativeBackup(path, data); err != nil {
						return err
					}
					if filepath.Base(path) == "uv" {
						return os.WriteFile(path, []byte("corrupted backup"), 0600)
					}
					return nil
				}
			}
			executed := false
			execution := ItemResult{}
			_, changed, err := applyNativeCLIWithOps(context.Background(), p.Items[0], f.o, f.state, runFunc(func(context.Context, Command) ([]byte, error) { executed = true; return nil, nil }), &execution, ops)
			if err == nil || err.Error() != "native_cli_backup_content_changed" || changed || executed || execution.Executed {
				t.Fatal("bad backup accepted", changed, execution, err)
			}
			data, _ := os.ReadFile(i.Path)
			if string(data) != "uv 0.13.0" {
				t.Fatal("installation changed despite bad backup")
			}
		})
	}
}

func TestNativeCLIRecoveryPreservesConcurrentEditsAndOriginalBackup(t *testing.T) {
	for _, mode := range []string{"during-first-file", "during-second-capture", "new-destination"} {
		t.Run(mode, func(t *testing.T) {
			f, i := nativeCLIFixture(t)
			p := f.plan(nativeSelection(i.ID))
			if err := os.MkdirAll(f.state, 0700); err != nil {
				t.Fatal(err)
			}
			second := filepath.Join(f.home, ".local/bin/uvx")
			ops := nativeCLIOps{read: readNativeFile, writeBackup: writeNativeBackup, rename: os.Rename, link: os.Link}
			ops.rename = func(from, to string) error {
				if mode == "during-second-capture" && from == second {
					f.write(second, "user edited second binary", 0755)
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if mode == "during-first-file" && from == i.Path {
					f.write(second, "user edited second binary", 0755)
				}
				if mode == "new-destination" && from == second {
					f.write(second, "user edited second binary", 0755)
				}
				return nil
			}
			execution := ItemResult{}
			backup, changed, err := applyNativeCLIWithOps(context.Background(), p.Items[0], f.o, f.state, runFunc(func(context.Context, Command) ([]byte, error) {
				f.write(i.Path, "uv 0.13.1", 0755)
				f.write(second, "uvx 0.13.1", 0755)
				f.write(audit.UVReceiptPath(f.home), uvReceipt(t, f.home, "0.13.1"), 0644)
				return nil, errors.New("controlled failure after update")
			}), &execution, ops)
			if err == nil || err.Error() != "native_cli_recovery_requires_review" || !changed {
				t.Fatal(changed, err)
			}
			current, readErr := os.ReadFile(second)
			if readErr != nil || string(current) != "user edited second binary" {
				t.Fatal("concurrent edit overwritten", string(current), readErr)
			}
			original, readErr := os.ReadFile(filepath.Join(backup, "uvx"))
			if readErr != nil || string(original) != "uvx 0.13.0" {
				t.Fatal("original backup lost", readErr)
			}
			manifest, readErr := os.ReadFile(filepath.Join(backup, "recovery.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var entries []nativeRecoveryFile
			if json.Unmarshal(manifest, &entries) != nil || len(entries) != 3 {
				t.Fatal("recovery locations missing")
			}
			displaced, readErr := os.ReadFile(entries[0].Displaced)
			if readErr != nil || string(displaced) != "uv 0.13.1" {
				t.Fatal("replaced object lost", readErr)
			}
		})
	}
}
