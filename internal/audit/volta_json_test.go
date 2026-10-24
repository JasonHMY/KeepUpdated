package audit

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestVoltaObjectRejectsUnsupportedDepth(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       bool
	}{
		{"flat-pin", `{"node":"22.1.0","npm":"10.0.0","pnpm":"10.18.3","yarn":"1.22.22"}`, true},
		{"default-platform", `{"node":{"runtime":"22.1.0","npm":"10.0.0"}}`, true},
		{"package-platform", `{"platform":{"node":"22.1.0","npm":"10.0.0"}}`, true},
		{"third-object", `{"future":{"nested":{"value":"1.0.0"}}}`, false},
		{"known-field-object", `{"node":{"runtime":{"value":"1.0.0"}}}`, false},
		{"small-deep-object", strings.Repeat(`{"future":`, 16) + `"1.0.0"` + strings.Repeat(`}`, 16), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := voltaObject([]byte(tc.data)); got != tc.want {
				t.Fatalf("voltaObject = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVoltaObjectRejectsDepthBeforeDecoding(t *testing.T) {
	data := []byte(`{"future":{"nested":{}}}`)
	var accepted bool
	allocs := testing.AllocsPerRun(10, func() {
		accepted = voltaObjectDepth(data, 2)
	})
	if accepted || allocs != 0 {
		t.Fatalf("unsupported object depth: accepted=%v, allocations=%g; want false, 0", accepted, allocs)
	}
}

func TestVoltaJSONStrictBoundedRecords(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		out        func() any
		want       bool
	}{
		{"default", `{"node":{"runtime":"22.1.0","npm":"10.0.0"},"pnpm":"10.18.3","yarn":"1.22.22"}`, func() any {
			return &struct {
				Node       *struct{ Runtime, Npm string }
				Pnpm, Yarn string
			}{}
		}, true},
		{"package", `{"name":"@scope/tool","version":"1.0.0","platform":{"node":"22.1.0","npm":"10.0.0","pnpm":"10.18.3","yarn":"1.22.22"},"bins":["tool"],"manager":"Npm"}`, func() any { return &voltaPackageRecord{} }, true},
		{"bin", `{"name":"tool","package":"@scope/tool","version":"1.0.0","platform":{"node":"22.1.0"},"manager":"Npm"}`, func() any { return &voltaBinRecord{} }, true},
		{"duplicate-root", `{"node":"22.1.0","node":"20.1.0"}`, func() any { return &voltaPlatform{} }, false},
		{"escaped-duplicate", `{"node":"22.1.0","\u006eode":"20.1.0"}`, func() any { return &voltaPlatform{} }, false},
		{"duplicate-nested", `{"platform":{"node":"22.1.0","node":"20.1.0"}}`, func() any { return &voltaPackageRecord{} }, false},
		{"unknown-field", `{"node":"22.1.0","future":true}`, func() any { return &voltaPlatform{} }, false},
		{"unknown-nested", `{"platform":{"node":"22.1.0","future":{"nested":{}}}}`, func() any { return &voltaPackageRecord{} }, false},
		{"wrong-type", `{"node":true}`, func() any { return &voltaPlatform{} }, false},
		{"nested-array", `{"node":[{"runtime":"22.1.0"}]}`, func() any { return &voltaPlatform{} }, false},
		{"trailing", `{"node":"22.1.0"} {}`, func() any { return &voltaPlatform{} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "record.json")
			write(t, path, tc.data)
			if got := voltaJSON(root, path, tc.out()); got != tc.want {
				t.Fatalf("voltaJSON = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVoltaProjectPinRejectsNestedInputAndKeepsValidPins(t *testing.T) {
	for _, tc := range []struct {
		name, pin string
		want      string
	}{
		{"all-tools", `{"node":"22.1.0","npm":"10.0.0","pnpm":"10.18.3","yarn":"1.22.22"}`, "volta_project_pin_recorded"},
		{"partial-pin", `{"pnpm":"10.18.3"}`, "volta_project_pin_recorded"},
		{"escaped-key", `{"\u006eode":"22.1.0"}`, "volta_project_pin_recorded"},
		{"duplicate", `{"node":"22.1.0","node":"20.1.0"}`, "volta_project_pin_unverified"},
		{"unknown", `{"node":"22.1.0","future":{"nested":{}}}`, "volta_project_pin_unverified"},
		{"wrong-type", `{"node":{"runtime":"22.1.0"}}`, "volta_project_pin_unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			project := filepath.Join(s.Config.Home, "project")
			path := filepath.Join(project, "package.json")
			write(t, path, `{"volta":`+tc.pin+`}`)
			s.Config.Projects = []string{project}
			s.voltaProjects()
			i := voltaItem(t, s.Report, path)
			if i.Health.Status != "unknown" || i.Health.Code != tc.want || i.Update.Code != "volta_owner_manual" || i.Active || i.ActivityKnown {
				t.Fatalf("pin semantics changed: %+v", i)
			}
			if len(s.Report.Queries) != 0 {
				t.Fatal("metadata validation ran a command", s.Report.Queries)
			}
		})
	}
}
