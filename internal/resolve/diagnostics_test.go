package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestResolverFailuresReachPlanWithoutBlockingOtherItemsOrLeakingDetails(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		failure      error
	}{
		{"timeout", "owner_query_timeout", context.DeadlineExceeded},
		{"network", "owner_network_unavailable", manage.CommandFailure{Code: "owner_network_unavailable"}},
		{"permission", "owner_permission_denied", os.ErrPermission},
		{"unknown", "owner_resolution_failed", errors.New("/Users/private --token SECRET https://user:secret@host?token=SECRET")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, item, binding, _ := nodeFixture(t, `{"name":"fixture","version":"2.0.0"}`)
			if err := os.MkdirAll(filepath.Dir(binding.ManagerPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binding.ManagerPath, []byte("manager fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			r.Options.ManagerPaths = map[string]string{"npm": binding.ManagerPath}
			item.Source, item.Health = "npm-public", audit.Result{Status: "ok"}
			other := item
			other.ID, other.Name, other.Identity = "other", "other", "other"
			other.Path = filepath.Join(filepath.Dir(item.Path), "other")
			if err := os.MkdirAll(other.Path, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(other.Path, "package.json"), []byte(`{"name":"other","version":"1.0.0"}`), 0644); err != nil {
				t.Fatal(err)
			}
			r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
				switch c.Args[0] {
				case "--version":
					return []byte("11.19.1"), nil
				case "root":
					return []byte(filepath.Dir(item.Path)), nil
				case "ls":
					return []byte(`{"dependencies":{"fixture":{"version":"1.0.0"},"other":{"version":"1.0.0"}}}`), nil
				case "config":
					if c.Args[2] == "tag" {
						return []byte("latest"), nil
					}
					if c.Args[2] != "registry" {
						return []byte("undefined"), nil
					}
					return []byte("https://registry.example.invalid"), nil
				case "view":
					if c.Args[1] == "fixture@latest" {
						return nil, tc.failure
					}
					return []byte(`{"name":"other","version":"2.0.0"}`), nil
				}
				t.Fatal(c.Args)
				return nil, nil
			})
			report := audit.Report{Items: []audit.Item{item, other}}
			selection := manage.Selection{Action: "update", All: true}
			targets, err := r.Resolve(context.Background(), report, selection)
			if err != nil || targets[item.ID].ResolutionError != tc.reason || targets[other.ID].Version != "2.0.0" {
				t.Fatalf("%+v %v", targets, err)
			}
			r.Options.ResolvedTargets = targets
			plan, err := manage.NewPlan(report, selection, r.Options)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range plan.Items {
				if p.ID == item.ID && (p.Reason != tc.reason || p.Status != "blocked" || p.TargetVersion != "") {
					t.Fatalf("%+v", p)
				}
				if p.ID == other.ID && p.Status != "ready" {
					t.Fatalf("unrelated item blocked: %+v", p)
				}
			}
			data, _ := json.Marshal(targets)
			for _, secret := range []string{"SECRET", "/Users/private", "--token", "user:secret"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("resolver leaked raw error")
				}
			}
		})
	}
}

func TestResolutionBindingFailureIsRetainedAndFailureFieldIsOptional(t *testing.T) {
	r, item, _, _ := nodeFixture(t, `{}`)
	// No manager exists in the isolated home or configured prefix.
	r.Options.BrewRoots = []string{filepath.Join(r.Options.Home, "absent-brew")}
	targets, err := r.Resolve(context.Background(), audit.Report{Items: []audit.Item{item}}, manage.Selection{Action: "update", All: true})
	if err != nil || targets[item.ID].ResolutionError != "owner_unavailable" {
		t.Fatalf("%+v %v", targets, err)
	}
	r.Options.ManagerPaths = map[string]string{"npm": filepath.Join(r.Options.Home, "missing-manager")}
	targets, err = r.Resolve(context.Background(), audit.Report{Items: []audit.Item{item}}, manage.Selection{Action: "update", All: true})
	if err != nil || targets[item.ID].ResolutionError != "owner_unavailable" {
		t.Fatalf("explicit missing owner: %+v %v", targets, err)
	}
	data, _ := json.Marshal(manage.ResolvedTarget{Version: "1.0.0"})
	if strings.Contains(string(data), "resolution_error") {
		t.Fatal("successful wire contract changed")
	}
	if manage.PlanSchema != "keepupdated/plan/v1" || audit.Schema != "keepupdated/v1" {
		t.Fatal("schema changed")
	}
}

func TestPythonCurrentRequiresPositiveOwnerEvidence(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		for _, scenario := range []string{"empty", "unrelated", "current", "updated", "duplicate", "malformed", "tls-output"} {
			t.Run(owner+"/"+scenario, func(t *testing.T) {
				calls := 0
				r, item, b := pythonResolverFixture(t, owner, "example", queryRunner(func(c manage.Command) ([]byte, error) {
					calls++
					args := strings.Join(c.Args, " ")
					if scenario == "updated" {
						return []byte(`[{"name":"example","version":"1.0.0","latest_version":"2.0.0"}]`), nil
					}
					if scenario == "tls-output" {
						return []byte("Could not fetch URL https://user:SECRET@host: SSL certificate error - skipping\n[]"), nil
					}
					if scenario == "malformed" {
						return []byte(`invalid`), nil
					}
					if strings.Contains(args, "--uptodate") {
						if scenario == "current" {
							return []byte(`[{"name":"example","version":"1.0.0"}]`), nil
						}
						if scenario == "duplicate" {
							return []byte(`[{"name":"example","version":"1.0.0"},{"name":"example","version":"1.0.0"}]`), nil
						}
					}
					if scenario == "unrelated" {
						return []byte(`[{"name":"unrelated","version":"1.0.0","latest_version":"2.0.0"}]`), nil
					}
					return []byte(`[]`), nil
				}))
				result, err := r.python(context.Background(), item, b)
				success := scenario == "updated" || scenario == "current" && owner == "pipx"
				if (err == nil) != success {
					t.Fatalf("%+v %v", result, err)
				}
				if success && (!result.OriginVerified || result.Version == "") {
					t.Fatal(result)
				}
				if scenario == "tls-output" && failureReason(err) != "owner_network_unavailable" {
					t.Fatal(err)
				}
				if !success && result.OriginVerified {
					t.Fatal("unverified query became current")
				}
				if owner == "pipx" && scenario == "empty" && (calls != 2 || failureReason(err) != "owner_candidate_unavailable") {
					t.Fatal(calls, err)
				}
			})
		}
	}
}

func TestApplicationQueryFailureReachesPlanAlongsideHealthyPackages(t *testing.T) {
	r, report, packages := performanceFixture(t)
	appPath := filepath.Join(r.Options.Home, "Applications/Example.app")
	if err := os.MkdirAll(filepath.Join(appPath, "Contents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appPath, "Contents/Info.plist"), []byte("controlled metadata"), 0644); err != nil {
		t.Fatal(err)
	}
	app := audit.Item{ID: "application", Kind: "application", Owner: "standalone", Source: "sparkle", Path: appPath, Identity: "com.example.fixture", Version: "1.0.0", Active: true}
	report.Items = append(report.Items, app)
	r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
		if c.Path == "/bin/ps" {
			return []byte("SECRET"), manage.CommandFailure{Code: "owner_permission_denied"}
		}
		return packages.Run(context.Background(), c)
	})
	var events []audit.Progress
	r.Options.Progress = func(p audit.Progress) { events = append(events, p) }
	selection := manage.Selection{Action: "update", All: true}
	targets, err := r.Resolve(context.Background(), report, selection)
	if err != nil || targets[app.ID].ResolutionError != "owner_permission_denied" || !targets[report.Items[0].ID].OriginVerified {
		t.Fatal(targets, err)
	}
	r.Options.ResolvedTargets = targets
	plan, err := manage.NewPlan(report, selection, r.Options)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.Items {
		if item.ID == app.ID && (item.Reason != "owner_permission_denied" || item.Status != "manual") {
			t.Fatal(item)
		}
		if item.ID == report.Items[0].ID && item.Status != "ready" {
			t.Fatal("healthy package lost", item)
		}
	}
	data, _ := json.Marshal(plan)
	if strings.Contains(string(data), "SECRET") {
		t.Fatal("private query details leaked into plan")
	}
	var finished audit.Progress
	for _, event := range events {
		if event.Phase == "resolve" && event.State == "finished" {
			finished = event
		}
	}
	if finished.Total == nil || finished.Completed != *finished.Total || finished.Unresolved != 1 {
		t.Fatalf("failed app was not counted as unresolved: %+v", finished)
	}
}
