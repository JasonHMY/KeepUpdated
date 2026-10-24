package audit

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoppedLaunchdServiceIsNotFailure(t *testing.T) {
	states, known := parseLaunchList("PID\tStatus\tLabel\n-\t78\texample.stopped\n321\t0\texample.running\n")
	if !known {
		t.Fatal(states)
	}
	for _, tc := range []struct {
		label, code string
		active      bool
	}{{"example.stopped", "service_loaded_not_running", false}, {"example.running", "service_running", true}, {"example.unloaded", "service_not_loaded", false}} {
		i := item("service", tc.label, "fixture", "user-launchagent")
		applyServiceMetadata(&i, map[string]any{"Label": tc.label}, states, known)
		if i.Health.Code != tc.code || i.Active != tc.active || !i.ActivityKnown {
			t.Fatal(i)
		}
	}
}
func TestLaunchdDefinitionPrivacyAndLogMetadataOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	write(t, p, "TOKEN=SECRET_NEVER_OUTPUT\n")
	i := item("service", "example", "fixture", "user-launchagent")
	applyServiceMetadata(&i, map[string]any{"Label": "example", "Disabled": true, "ProgramArguments": []any{"/bin/sh", "-c", "TOKEN=SECRET_NEVER_OUTPUT"}, "EnvironmentVariables": map[string]any{"TOKEN": "SECRET_NEVER_OUTPUT"}, "StandardOutPath": p}, nil, true)
	b, _ := json.Marshal(i)
	if strings.Contains(string(b), "SECRET_NEVER_OUTPUT") {
		t.Fatal("service secret escaped")
	}
	if !strings.Contains(string(b), "log-size:StandardOutPath=") {
		t.Fatal("log size missing")
	}
	if i.Active || i.Health.Code != "service_not_loaded" {
		t.Fatal(i)
	}
}
func TestLaunchdDomainAndInvalidQueryStayUnknown(t *testing.T) {
	if _, known := parseLaunchList("unrecognized output"); known {
		t.Fatal("invalid query parsed")
	}
	i := item("service", "example", "fixture", "system-daemon")
	applyServiceMetadata(&i, map[string]any{"Label": "example"}, nil, false)
	if i.Health.Status != "unknown" || i.ActivityKnown {
		t.Fatal(i)
	}
}
