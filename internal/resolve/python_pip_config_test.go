package resolve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestPipxCommandSourcesStopBeforeCandidateQuery(t *testing.T) {
	for _, section := range []string{"list", "install", "DEFAULT"} {
		for _, key := range []string{"index-url", "extra-index-url", "find-links", "no-index"} {
			for _, spelling := range []string{key, strings.ReplaceAll(key, "-", "_"), "--" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))} {
				t.Run(section+"/"+spelling, func(t *testing.T) {
					queries := 0
					r, item, b := pythonResolverFixture(t, "pipx", "example", queryRunner(func(c manage.Command) ([]byte, error) {
						queries++
						return []byte(`[{"name":"example","version":"1.0.0","latest_version":"2.0.0"}]`), nil
					}))
					value := "https://candidate.invalid/simple"
					if key == "no-index" {
						value = "true"
					}
					config := "[global]\nindex-url = https://original.invalid/simple\n[" + section + "]\n" + spelling + " = " + value + "\n"
					if err := os.WriteFile(filepath.Join(b.Scope, "pip.conf"), []byte(config), 0644); err != nil {
						t.Fatal(err)
					}
					report := audit.Report{Items: []audit.Item{item}}
					selection := manage.Selection{Action: "update", All: true}
					targets, err := r.Resolve(context.Background(), report, selection)
					if err != nil {
						t.Fatal(err)
					}
					options := r.Options
					options.ResolvedTargets = targets
					plan, err := manage.NewPlan(report, selection, options)
					if err != nil {
						t.Fatal(err)
					}
					if len(plan.Items) != 1 || queries != 0 || targets[item.ID].OriginVerified || plan.Items[0].Status != "manual" || plan.Items[0].Reason != "python_configuration_requires_review" {
						t.Fatalf("command-specific source accepted: queries=%d target=%v status=%s", queries, targets[item.ID].OriginVerified, plan.Items[0].Status)
					}
				})
			}
		}
	}
}

func TestPipxSavedSourceMismatchCannotDispatchAfterRevalidation(t *testing.T) {
	for _, key := range []string{"index-url", "extra_index_url", "find-links", "no_index"} {
		t.Run(key, func(t *testing.T) {
			queries, mutations := 0, 0
			r, item, b := pythonResolverFixture(t, "pipx", "example", queryRunner(func(c manage.Command) ([]byte, error) {
				for _, v := range c.Env {
					if strings.HasPrefix(v, "PIP_INDEX_URL=") || strings.HasPrefix(v, "PIP_CONFIG_FILE=") {
						t.Fatal("configuration silently overridden")
					}
				}
				if len(c.Args) > 0 && c.Args[0] == "upgrade" {
					mutations++
					return nil, errors.New("controlled runner stops before mutation")
				}
				queries++
				return []byte(`[{"name":"example","version":"1.0.0","latest_version":"2.0.0"}]`), nil
			}))
			listValue, installValue := "https://candidate.invalid/simple", "https://installation.invalid/simple"
			if key == "no_index" {
				listValue, installValue = "true", "false"
			}
			path := filepath.Join(b.Scope, "pip.conf")
			config := "[list]\n" + key + " = " + listValue + "\n[install]\n" + key + " = " + installValue + "\n"
			if err := os.WriteFile(path, []byte(config), 0644); err != nil {
				t.Fatal(err)
			}
			policy, err := r.pythonPolicy(context.Background(), item, b, "update")
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{filepath.Join(r.Options.Home, "Library/Application Support/pip/pip.conf"), filepath.Join(r.Options.Home, ".config/pip/pip.conf"), filepath.Join(r.Options.Home, ".pip/pip.conf"), path, "/Library/Application Support/pip/pip.conf"}
			digest, err := configDigest("pipx", paths)
			if err != nil {
				t.Fatal(err)
			}
			// Model a saved preview from the former whole-file-hash resolver. All receipt
			// evidence is valid, but these unchanged bytes have different list/install sources.
			target := manage.ResolvedTarget{Python: policy, Version: "2.0.0", Source: "manager-config:" + digest, Provider: "pipx", ManagerPath: b.ManagerPath, Scope: b.Scope, OriginVerified: true, Reason: "configured_manager_source"}
			options := r.Options
			options.ResolvedTargets = map[string]manage.ResolvedTarget{item.ID: target}
			report := audit.Report{Items: []audit.Item{item}}
			selection := manage.Selection{Action: "update", IDs: []string{item.ID}}
			plan, err := manage.NewPlan(report, selection, options)
			if err != nil || len(plan.Items) != 1 || plan.Items[0].Status != "ready" {
				t.Fatal("invalid saved-preview fixture", err)
			}
			engine := manage.Engine{Options: options, Runner: r.Runner, StateDir: filepath.Join(options.Home, "actions"), Snapshot: func(context.Context) (audit.Report, error) { return report, nil }, ResolveTargets: r.Resolve}
			_, err = manage.Apply(context.Background(), plan, plan.PlanID, engine)
			if err == nil || queries != 0 || mutations != 0 {
				t.Fatalf("saved mismatch reached execution: error=%v queries=%d mutations=%d", err, queries, mutations)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != config {
				t.Fatal("source configuration changed", err)
			}
		})
	}
}

func TestPipxDefaultAndGlobalSourcesRemainReady(t *testing.T) {
	for _, config := range []string{
		"",
		"# defaults\n; no overrides\n",
		"[global]\nindex-url = https://original.invalid/simple\n",
		"[global]\nextra_index_url = https://additional.invalid/simple\n",
		"[global]\n    --INDEX_URL: https://original.invalid/simple\n",
		"[global]\nfind-links =\n    https://wheels.invalid/one\n    https://wheels.invalid/two\n",
		"[global]\nno_index = false\n[list]\nformat = json\n[install]\ntimeout = 20\n",
	} {
		t.Run(config, func(t *testing.T) {
			queries := 0
			r, item, b := pythonResolverFixture(t, "pipx", "example", queryRunner(func(c manage.Command) ([]byte, error) {
				queries++
				return []byte(`[{"name":"example","version":"1.0.0","latest_version":"2.0.0"}]`), nil
			}))
			if config != "" {
				if err := os.WriteFile(filepath.Join(b.Scope, "pip.conf"), []byte(config), 0644); err != nil {
					t.Fatal(err)
				}
			}
			report := audit.Report{Items: []audit.Item{item}}
			selection := manage.Selection{Action: "update", All: true}
			targets, err := r.Resolve(context.Background(), report, selection)
			if err != nil {
				t.Fatal(err)
			}
			options := r.Options
			options.ResolvedTargets = targets
			plan, err := manage.NewPlan(report, selection, options)
			if err != nil || len(plan.Items) != 1 {
				t.Fatal(err)
			}
			if queries != 1 || !targets[item.ID].OriginVerified || plan.Items[0].Status != "ready" || plan.Items[0].TargetVersion != "2.0.0" {
				t.Fatalf("default/global source lost support: queries=%d status=%s reason=%s", queries, plan.Items[0].Status, plan.Items[0].Reason)
			}
		})
	}
}

func TestPipxContinuationDoesNotChangeCommandScope(t *testing.T) {
	for _, tc := range []struct {
		config    string
		supported bool
	}{
		{"[list]\ntrusted-host = candidate.invalid\n    [global]\nindex_url = https://candidate.invalid/simple\n", false},
		{"[global]\nfind-links =\n    https://wheels.invalid\n    [list]\nextra-index-url = https://additional.invalid/simple\n", true},
		{"[install]\n    index_url = https://installation.invalid/simple\n", false},
	} {
		if got := pipCandidateConfigSupportsQuery([]byte(tc.config), ""); got != tc.supported {
			t.Fatalf("continuation changed source scope: got=%v want=%v", got, tc.supported)
		}
	}
}
