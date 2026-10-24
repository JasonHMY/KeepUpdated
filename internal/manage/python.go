package manage

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func pythonPlan(p PlanItem) bool { return p.Provider == "pipx" || p.Provider == "uv" }

func pythonEnvironment(i audit.Item) string {
	for _, e := range i.Evidence {
		if strings.HasPrefix(e, "environment:") {
			return strings.TrimPrefix(e, "environment:")
		}
	}
	return ""
}

func pythonRecordInScope(i audit.Item, p PlanItem) bool {
	if i.Kind != "python-package" || i.Owner != p.Binding.Owner || normalizePython(i.Identity) != normalizePython(p.Binding.Identity) || i.Identity == "" || pythonEnvironment(i) != p.Binding.Scope {
		return false
	}
	rel, err := filepath.Rel(p.Binding.Scope, i.Path)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 4 || parts[0] != "lib" || !strings.HasPrefix(parts[1], "python") || parts[2] != "site-packages" || !strings.HasSuffix(parts[3], ".dist-info") {
		return false
	}
	return true
}

func pythonPostCandidate(i audit.Item, p PlanItem) bool {
	if !pythonRecordInScope(i, p) {
		return false
	}
	rel, _ := filepath.Rel(p.Binding.Scope, i.Path)
	st, err := os.Lstat(i.Path)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	realScope, se := filepath.EvalSymlinks(p.Binding.Scope)
	realRecord, re := filepath.EvalSymlinks(i.Path)
	return se == nil && re == nil && realRecord == filepath.Join(realScope, rel)
}

func pythonPostInstall(report audit.Report, p PlanItem) (*audit.Item, bool) {
	var matched *audit.Item
	for n := range report.Items {
		if pythonRecordInScope(report.Items[n], p) {
			if matched != nil {
				return nil, false
			}
			matched = &report.Items[n]
		}
	}
	return matched, true
}

func pythonMetadataMatches(i audit.Item) bool {
	b, err := readMetadata(filepath.Join(i.Path, "METADATA"))
	if err != nil {
		return false
	}
	name, v := "", ""
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if key == "Name" {
			if name != "" {
				return false
			}
			name = value
		}
		if key == "Version" {
			if v != "" {
				return false
			}
			v = value
		}
	}
	return scanner.Err() == nil && normalizePython(name) == normalizePython(i.Identity) && v == i.Version
}

func pythonConstraintPath(state string, p PlanItem) string {
	return filepath.Join(state, "python-"+hashText(p.Binding.Scope+"\x00"+p.Binding.PackageName+"\x00"+p.TargetVersion)+".constraints")
}

func pythonRequirementHash(i audit.Item, env string) (string, error) {
	if i.Owner == "pipx" {
		var m struct {
			Main struct {
				Spec string `json:"package_or_url"`
			} `json:"main_package"`
		}
		b, err := readMetadata(filepath.Join(env, "pipx_metadata.json"))
		if err != nil || json.Unmarshal(b, &m) != nil {
			return "", errors.New("invalid pipx requirement")
		}
		name, _, ok := pythonRequirement(m.Main.Spec)
		if !ok || name != normalizePython(i.Identity) {
			return "", errors.New("invalid pipx requirement")
		}
		return hashText(m.Main.Spec), nil
	}
	b, err := readMetadata(filepath.Join(env, "uv-receipt.toml"))
	if err != nil {
		return "", err
	}
	doc, err := parseUVReceipt(b)
	if err != nil {
		return "", err
	}
	tool, ok := doc["tool"].(map[string]any)
	if !ok {
		return "", errors.New("invalid uv requirement")
	}
	data, err := json.Marshal(tool["requirements"])
	if err != nil {
		return "", err
	}
	return hashText(string(data)), nil
}

// PIP_CONSTRAINT and PIP_ONLY_BINARY affect this invocation without persisting
// temporary flags or paths in pipx_metadata.json's pip_args.
func preparePipxConstraint(state string, p PlanItem) error {
	if !filepath.IsAbs(state) || !simpleName.MatchString(p.Binding.PackageName) || !version.MatchString(p.TargetVersion) {
		return errors.New("invalid Python constraint binding")
	}
	content := p.Binding.PackageName + "==" + p.TargetVersion + "\n"
	var metadata struct {
		Main struct {
			Spec string `json:"package_or_url"`
		} `json:"main_package"`
	}
	b, err := readMetadata(filepath.Join(p.Binding.Scope, "pipx_metadata.json"))
	if err != nil || json.Unmarshal(b, &metadata) != nil {
		return errors.New("Python tool receipt unavailable")
	}
	if p.Binding.PythonRequirementFingerprint != "" && hashText(metadata.Main.Spec) != p.Binding.PythonRequirementFingerprint {
		return errors.New("Python requirement changed since preview")
	}
	parsed := regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[A-Za-z0-9_,.-]+\])?([<>=!~][A-Za-z0-9.*<>=!~, -]+)?$`).FindStringSubmatch(metadata.Main.Spec)
	if len(parsed) != 3 || normalizePython(parsed[1]) != normalizePython(p.Binding.Identity) {
		return errors.New("Python original requirement unavailable")
	}
	if parsed[2] != "" {
		content += parsed[1] + parsed[2] + "\n"
	}
	path := pythonConstraintPath(state, p)
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		data, e := readMetadata(path)
		st, se := os.Lstat(path)
		if e != nil || se != nil || st.Mode().Perm() != 0600 || string(data) != content {
			return errors.New("Python constraint file unavailable")
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
			return errors.New("Python constraint file owner mismatch")
		}
		return nil
	}
	f := os.NewFile(uintptr(fd), path)
	_, we := f.WriteString(content)
	se := f.Sync()
	ce := f.Close()
	if we != nil || se != nil || ce != nil {
		return errors.New("Python constraint file write failed")
	}
	return nil
}
