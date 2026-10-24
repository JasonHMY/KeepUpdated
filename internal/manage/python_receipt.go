package manage

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// PythonPolicy contains stable installation intent, not observed package versions
// or sampling times. Old saved plans without this evidence require a new preview.
type PythonPolicy struct {
	EnvironmentRealPath    string             `json:"environment_real_path"`
	ManagerVersion         string             `json:"manager_version"`
	ManagerFingerprint     string             `json:"manager_fingerprint"`
	Bin                    string             `json:"bin"`
	Schema                 string             `json:"schema"`
	Backend                string             `json:"backend"`
	IntentFingerprint      string             `json:"intent_fingerprint"`
	InterpreterFingerprint string             `json:"interpreter_fingerprint"`
	Entrypoints            []PythonEntrypoint `json:"entrypoints"`
}
type PythonEntrypoint struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type pythonReceipt struct {
	schema, backend, requirement, specifier, intent string
	pinned                                          bool
	entries                                         []PythonEntrypoint
	extras                                          []string
}

func pythonFailure(code string) error { return CommandFailure{Code: code} }

func receiptKeys(m map[string]any, allowed string) bool {
	for key := range m {
		if !strings.Contains(" "+allowed+" ", " "+key+" ") {
			return false
		}
	}
	return true
}
func receiptString(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func emptyReceiptValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func readPythonReceipt(i audit.Item, env, bin, action string) (pythonReceipt, error) {
	r := pythonReceipt{}
	fail := func(code string) (pythonReceipt, error) { return r, pythonFailure(code) }
	if _, err := os.Lstat(filepath.Join(i.Path, "direct_url.json")); !os.IsNotExist(err) {
		return fail("owner_source_requires_review")
	}
	if i.Owner == "pipx" {
		data, err := readMetadata(filepath.Join(env, "pipx_metadata.json"))
		if err != nil {
			return fail("python_receipt_schema_unsupported")
		}
		value, err := uniqueReceiptJSON(data)
		m, ok := value.(map[string]any)
		if err != nil || !ok || !receiptKeys(m, "main_package python_version source_interpreter venv_args injected_packages pipx_metadata_version environment backend exposure_enabled") {
			return fail("python_receipt_schema_unsupported")
		}
		r.schema = receiptString(m, "pipx_metadata_version")
		if r.schema != "0.5" && r.schema != "0.12" {
			return fail("python_receipt_schema_unsupported")
		}
		r.backend = "pip"
		if r.schema == "0.12" {
			r.backend = receiptString(m, "backend")
			if m["exposure_enabled"] != true {
				return fail("python_entrypoints_require_review")
			}
		}
		if r.backend != "pip" || (r.schema == "0.5" && m["backend"] != nil) {
			return fail("python_backend_unsupported")
		}
		injected, injectedOK := m["injected_packages"].(map[string]any)
		if !injectedOK || len(injected) != 0 {
			return fail("python_injected_tools_require_review")
		}
		venvArgs, venvArgsOK := m["venv_args"].([]any)
		if !venvArgsOK || len(venvArgs) != 0 {
			return fail("python_receipt_schema_unsupported")
		}
		main, ok := m["main_package"].(map[string]any)
		if !ok || !receiptKeys(main, "package package_or_url package_version pip_args include_dependencies include_apps apps app_paths apps_of_dependencies app_paths_of_dependencies man_pages man_paths man_pages_of_dependencies man_paths_of_dependencies suffix pinned expected_apps lock_file include_resources_from cooldown_days completions completion_paths completions_of_dependencies completion_paths_of_dependencies") {
			return fail("python_receipt_schema_unsupported")
		}
		if main["lock_file"] != nil {
			return fail("python_locked_tool_requires_review")
		}
		for _, key := range []string{"apps_of_dependencies", "app_paths_of_dependencies", "man_pages", "man_paths", "man_pages_of_dependencies", "man_paths_of_dependencies", "include_resources_from", "completions", "completion_paths", "completions_of_dependencies", "completion_paths_of_dependencies"} {
			if !emptyReceiptValue(main[key]) {
				return fail("python_entrypoints_require_review")
			}
		}
		if main["include_apps"] != true || main["include_dependencies"] != false || receiptString(main, "suffix") != "" {
			return fail("python_entrypoints_require_review")
		}
		pipArgs, pipArgsOK := main["pip_args"].([]any)
		if main["cooldown_days"] != nil || !pipArgsOK || len(pipArgs) != 0 {
			return fail("owner_source_requires_review")
		}
		if normalizePython(receiptString(main, "package")) != normalizePython(i.Identity) || receiptString(main, "package_version") != i.Version {
			return fail("python_receipt_schema_unsupported")
		}
		r.requirement = receiptString(main, "package_or_url")
		name, spec, ok := pythonRequirement(r.requirement)
		if !ok || name != normalizePython(i.Identity) {
			return fail("python_requirement_constraints_require_review")
		}
		if start := strings.IndexByte(r.requirement, '['); start >= 0 {
			end := strings.IndexByte(r.requirement, ']')
			r.extras = strings.Split(r.requirement[start+1:end], ",")
		}
		r.specifier = spec
		if v, exists := main["pinned"]; exists {
			pinned, ok := v.(bool)
			if !ok {
				return fail("python_receipt_schema_unsupported")
			}
			r.pinned = pinned
		}
		apps, aok := main["apps"].([]any)
		paths, pok := main["app_paths"].([]any)
		if !aok || !pok || len(apps) != len(paths) {
			return fail("python_entrypoints_require_review")
		}
		seen := map[string]bool{}
		for n, v := range apps {
			name, ok := v.(string)
			path, okp := paths[n].(map[string]any)
			if !ok || !okp || !simpleName.MatchString(name) || seen[name] || !receiptKeys(path, "__type__ __Path__") || path["__type__"] != "Path" || path["__Path__"] != filepath.Join(env, "bin", name) {
				return fail("python_entrypoints_require_review")
			}
			seen[name] = true
			r.entries = append(r.entries, PythonEntrypoint{name, filepath.Join(bin, name)})
		}
		if expected, exists := main["expected_apps"]; exists {
			a, ok := expected.([]any)
			if !ok {
				return fail("python_entrypoints_require_review")
			}
			for _, v := range a {
				s, ok := v.(string)
				if !ok || !seen[s] {
					return fail("python_entrypoints_require_review")
				}
			}
		}
		if receiptString(m, "python_version") == "" || receiptPath(m["source_interpreter"]) == "" {
			return fail("python_interpreter_unverified")
		}
		sourceInterpreter, sourceErr := filepath.EvalSymlinks(receiptPath(m["source_interpreter"]))
		envInterpreter, envErr := filepath.EvalSymlinks(filepath.Join(env, "bin/python"))
		if sourceErr != nil || envErr != nil || sourceInterpreter != envInterpreter {
			return fail("python_interpreter_unverified")
		}
		if r.schema == "0.12" && m["environment"] != filepath.Base(env) {
			return fail("python_receipt_schema_unsupported")
		}
		// Installed version is an observation; every remaining recorded option is intent.
		delete(main, "package_version")
		stable, _ := json.Marshal(m)
		r.intent = hashText(string(stable))
	} else {
		if _, err := os.Lstat(filepath.Join(env, "uv.lock")); !os.IsNotExist(err) {
			return fail("python_locked_tool_requires_review")
		}
		data, err := readMetadata(filepath.Join(env, "uv-receipt.toml"))
		if err != nil {
			return fail("python_receipt_schema_unsupported")
		}
		doc, err := parseUVReceipt(data)
		if err != nil {
			return fail("python_receipt_schema_unsupported")
		}
		tool, ok := doc["tool"].(map[string]any)
		if !ok || !receiptKeys(tool, "requirements constraints overrides excludes build-constraint-dependencies python entrypoints") {
			return fail("python_receipt_schema_unsupported")
		}
		for _, key := range []string{"constraints", "overrides", "excludes", "build-constraint-dependencies"} {
			if !emptyReceiptValue(tool[key]) {
				return fail("python_receipt_constraints_require_review")
			}
		}
		if py, exists := tool["python"]; exists {
			if s, ok := py.(string); !ok || s == "" {
				return fail("python_interpreter_unverified")
			}
		}
		if opts, exists := doc["tool.options"]; exists {
			options := opts.(map[string]any)
			if !receiptKeys(options, "index index-url extra-index-url default-index find-links no-index index-strategy no-build") {
				return fail("python_receipt_schema_unsupported")
			}
			for key, value := range options {
				if key == "no-build" {
					if _, ok := value.(bool); !ok {
						return fail("python_receipt_schema_unsupported")
					}
					continue
				}
				if action != "uninstall" {
					return fail("uv_receipt_source_settings_require_review")
				}
			}
		}
		reqs, ok := tool["requirements"].([]any)
		if !ok || len(reqs) != 1 {
			return fail("python_injected_tools_require_review")
		}
		req, ok := reqs[0].(map[string]any)
		if !ok || !receiptKeys(req, "name extras specifier") || normalizePython(receiptString(req, "name")) != normalizePython(i.Identity) {
			return fail("python_receipt_schema_unsupported")
		}
		r.requirement = receiptString(req, "name")
		r.specifier = receiptString(req, "specifier")
		if v, exists := req["specifier"]; exists {
			if _, ok := v.(string); !ok {
				return fail("python_requirement_constraints_require_review")
			}
		}
		if extras, exists := req["extras"]; exists {
			a, ok := extras.([]any)
			if !ok {
				return fail("python_receipt_schema_unsupported")
			}
			for _, v := range a {
				s, ok := v.(string)
				if !ok || !simpleName.MatchString(s) {
					return fail("python_receipt_schema_unsupported")
				}
				r.extras = append(r.extras, s)
			}
		}
		entries, ok := tool["entrypoints"].([]any)
		if !ok {
			return fail("python_entrypoints_require_review")
		}
		seen := map[string]bool{}
		for _, v := range entries {
			entry, ok := v.(map[string]any)
			name := receiptString(entry, "name")
			if !ok || !receiptKeys(entry, "name install-path from") || !simpleName.MatchString(name) || seen[name] || receiptString(entry, "install-path") != filepath.Join(bin, name) {
				return fail("python_entrypoints_require_review")
			}
			if from, exists := entry["from"]; exists {
				s, ok := from.(string)
				if !ok || normalizePython(s) != normalizePython(i.Identity) {
					return fail("python_entrypoints_require_review")
				}
			}
			seen[name] = true
			r.entries = append(r.entries, PythonEntrypoint{name, filepath.Join(bin, name)})
		}
		r.schema = "uv-tool-0.13.1"
		r.backend = "uv"
		stable, _ := json.Marshal(doc)
		r.intent = hashText(string(stable))
	}
	sort.Slice(r.entries, func(a, b int) bool { return r.entries[a].Name < r.entries[b].Name })
	if action != "uninstall" {
		if !pythonExtrasPresent(i, r.extras) {
			return fail("python_requirement_installation_mismatch")
		}
		if r.pinned || strings.Contains(r.specifier, "==") {
			return fail("owner_version_pinned")
		}
		if _, known := pythonSpecifierAllows(r.specifier, i.Version); !known {
			return fail("python_requirement_constraints_require_review")
		}
		if allowed, _ := pythonSpecifierAllows(r.specifier, i.Version); !allowed {
			return fail("python_requirement_installation_mismatch")
		}
	}
	return r, nil
}

// ReadPythonPolicy binds an exact, verified manager release and its receipt.
// It never invokes an interpreter or manager; callers query --version separately.
func ReadPythonPolicy(i audit.Item, b Binding, o Options, managerVersion, action string) (*PythonPolicy, error) {
	if !pythonMetadataMatches(i) {
		return nil, pythonFailure("owner_installation_changed")
	}
	realScope, scopeErr := filepath.EvalSymlinks(b.Scope)
	realRecord, recordErr := filepath.EvalSymlinks(i.Path)
	relative, relErr := filepath.Rel(b.Scope, i.Path)
	expected := PlanItem{Binding: Binding{Scope: b.Scope, Owner: i.Owner, Identity: i.Identity}}
	if scopeErr != nil || recordErr != nil || relErr != nil || !pythonRecordInScope(i, expected) || realRecord != filepath.Join(realScope, relative) {
		return nil, pythonFailure("owner_installation_changed")
	}
	r, err := readPythonReceipt(i, b.Scope, managerBin(i.Owner, b, normalizeOptions(o)), action)
	if err != nil {
		return nil, err
	}
	supported := i.Owner == "pipx" && ((managerVersion == "1.8.0" && r.schema == "0.5") || (managerVersion == "1.18.4" && r.schema == "0.12")) || i.Owner == "uv" && managerVersion == "0.13.1" && r.schema == "uv-tool-0.13.1"
	if !supported {
		return nil, pythonFailure("python_manager_capability_unsupported")
	}
	digest, err := fileHash(b.ManagerPath)
	if err != nil || digest != b.ManagerFingerprint {
		return nil, pythonFailure("owner_installation_changed")
	}
	interpreter, err := pythonInterpreterHash(b.Scope)
	if err != nil {
		return nil, pythonFailure("python_interpreter_unverified")
	}
	if _, err = pythonEntrypointHash(i, b.Scope, r.entries, managerVersion); err != nil {
		return nil, pythonFailure("python_entrypoints_require_review")
	}
	return &PythonPolicy{EnvironmentRealPath: realScope, Bin: managerBin(i.Owner, b, normalizeOptions(o)), ManagerVersion: managerVersion, ManagerFingerprint: digest, Schema: r.schema, Backend: r.backend, IntentFingerprint: r.intent, InterpreterFingerprint: interpreter, Entrypoints: r.entries}, nil
}
func validPythonPolicy(i audit.Item, b Binding, o Options, p *PythonPolicy, action string) bool {
	if p == nil {
		return false
	}
	current, err := ReadPythonPolicy(i, b, o, p.ManagerVersion, action)
	return err == nil && reflect.DeepEqual(current, p)
}
func PythonTargetAllowed(i audit.Item, b Binding, o Options, target string) error {
	r, err := readPythonReceipt(i, b.Scope, managerBin(i.Owner, b, normalizeOptions(o)), "update")
	if err != nil {
		return err
	}
	allowed, known := pythonSpecifierAllows(r.specifier, target)
	if !known {
		return pythonFailure("python_requirement_constraints_require_review")
	}
	if !allowed {
		return pythonFailure("python_target_outside_constraints")
	}
	return nil
}
func pythonInterpreterHash(env string) (string, error) {
	python := filepath.Join(env, "bin/python")
	real, err := filepath.EvalSymlinks(python)
	if err != nil {
		return "", err
	}
	if err = ownedPath(real, true); err != nil {
		return "", err
	}
	binary, err := fileHash(real)
	if err != nil {
		return "", err
	}
	cfg, err := fileHash(filepath.Join(env, "pyvenv.cfg"))
	if err != nil {
		return "", err
	}
	return hashText(real + "\x00" + binary + "\x00" + cfg), nil
}

var pythonRequirementPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[A-Za-z0-9_,.-]+\])?((?:[<>=!~].*)?)$`)

func pythonRequirement(s string) (string, string, bool) {
	m := pythonRequirementPattern.FindStringSubmatch(s)
	if len(m) != 3 {
		return "", "", false
	}
	return normalizePython(m[1]), m[2], true
}

// Only numeric conjunctions are supported. Markers, epochs, prereleases, wildcards
// and compatible-release operators need native solving, not an approximation.
func pythonSpecifierAllows(spec, target string) (bool, bool) {
	numeric := regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)
	if !numeric.MatchString(target) {
		return false, false
	}
	if spec == "" {
		return true, true
	}
	allowed := true
	for _, part := range strings.Split(spec, ",") {
		m := regexp.MustCompile(`^(<=|>=|!=|==|<|>)\s*([0-9]+(?:\.[0-9]+)*)$`).FindStringSubmatch(strings.TrimSpace(part))
		if len(m) != 3 {
			return false, false
		}
		cmp, known := audit.CompareVersions(target, m[2])
		if !known {
			return false, false
		}
		switch m[1] {
		case "<=":
			allowed = allowed && cmp <= 0
		case ">=":
			allowed = allowed && cmp >= 0
		case "<":
			allowed = allowed && cmp < 0
		case ">":
			allowed = allowed && cmp > 0
		case "==":
			allowed = allowed && cmp == 0
		case "!=":
			allowed = allowed && cmp != 0
		}
	}
	return allowed, true
}

func uniqueReceiptJSON(data []byte) (any, error) {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 12 {
			return nil, errors.New("receipt nesting unsupported")
		}
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		if delimiter, ok := t.(json.Delim); ok {
			if delimiter == '{' {
				m := map[string]any{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return nil, err
					}
					k, ok := key.(string)
					if !ok {
						return nil, errors.New("invalid key")
					}
					if _, exists := m[k]; exists {
						return nil, errors.New("duplicate key")
					}
					v, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					m[k] = v
				}
				_, err = d.Token()
				return m, err
			}
			if delimiter == '[' {
				a := []any{}
				for d.More() {
					v, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					a = append(a, v)
				}
				_, err = d.Token()
				return a, err
			}
			return nil, errors.New("unexpected delimiter")
		}
		return t, nil
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing receipt data")
	}
	return value, nil
}

func receiptPath(v any) string {
	if s, ok := v.(string); ok && filepath.IsAbs(s) {
		return s
	}
	if m, ok := v.(map[string]any); ok && receiptKeys(m, "__type__ __Path__") && m["__type__"] == "Path" {
		if s := receiptString(m, "__Path__"); filepath.IsAbs(s) {
			return s
		}
	}
	return ""
}

func pythonPolicyAfter(i audit.Item, p PlanItem) bool {
	policy := p.Binding.Python
	if policy == nil {
		return false
	}
	return validPythonPolicy(i, p.Binding, Options{ManagerBins: map[string]string{i.Owner: policy.Bin}}, policy, "update")
}

func pythonExtrasPresent(i audit.Item, extras []string) bool {
	if len(extras) == 0 {
		return true
	}
	data, err := readMetadata(filepath.Join(i.Path, "METADATA"))
	if err != nil {
		return false
	}
	provided := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(key, "Provides-Extra") {
			provided[normalizePython(strings.TrimSpace(value))] = true
		}
	}
	for _, extra := range extras {
		if !provided[normalizePython(extra)] {
			return false
		}
	}
	return true
}
