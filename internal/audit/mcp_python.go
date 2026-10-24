package audit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func pythonMCPBin(candidate Item, entry, command string, config Config) bool {
	if candidate.Kind != "python-package" || candidate.Owner != "pipx" && candidate.Owner != "uv" || candidate.Identity == "" {
		return false
	}
	env := ""
	for _, e := range candidate.Evidence {
		if strings.HasPrefix(e, "environment:") {
			env = strings.TrimPrefix(e, "environment:")
		}
	}
	bases := []string{filepath.Join(config.Home, ".local/share/uv/tools")}
	if candidate.Owner == "pipx" {
		bases = []string{filepath.Join(config.Home, ".local/share/pipx/venvs"), filepath.Join(config.Home, ".local/pipx/venvs")}
	}
	if root := config.ManagerRoots[candidate.Owner]; validManagerRoot(root) {
		bases = append(bases, root)
	}
	bin := filepath.Join(config.Home, ".local/bin")
	if configured := config.ManagerBins[candidate.Owner]; validManagerRoot(configured) {
		bin = configured
	}
	allowed := false
	for _, base := range bases {
		allowed = allowed || filepath.Dir(env) == base
	}
	name := filepath.Base(entry)
	normalize := func(s string) string { return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(s)) }
	if !allowed || normalize(candidate.Identity) != filepath.Base(env) || !within(candidate.Path, env) || real(candidate.Path) != candidate.RealPath {
		return false
	}
	declared := filepath.Join(env, "bin", name)
	if entry != declared && entry != filepath.Join(bin, name) || real(declared) != command || command != filepath.Join(real(env), "bin", name) {
		return false
	}
	metadata, err := readSmall(filepath.Join(candidate.Path, "entry_points.txt"))
	if err != nil {
		return false
	}
	section := ""
	matches := 0
	for _, line := range strings.Split(string(metadata), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section == "[console_scripts]" {
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == name && strings.TrimSpace(value) != "" {
				matches++
			}
		}
	}
	if matches != 1 || !pythonEntryUnmodified(candidate.Path, declared, env) {
		return false
	}
	if candidate.Owner == "pipx" {
		var receipt struct {
			Main struct {
				Package string
				Version string `json:"package_version"`
				Apps    []string
				Paths   []struct {
					Path string `json:"__Path__"`
				} `json:"app_paths"`
			} `json:"main_package"`
		}
		b, err := readSmall(filepath.Join(env, "pipx_metadata.json"))
		if err != nil || json.Unmarshal(b, &receipt) != nil || normalize(receipt.Main.Package) != normalize(candidate.Identity) || receipt.Main.Version != candidate.Version {
			return false
		}
		apps, paths := 0, 0
		for _, app := range receipt.Main.Apps {
			if app == name {
				apps++
			}
		}
		for _, p := range receipt.Main.Paths {
			if p.Path == declared {
				paths++
			}
		}
		return apps == 1 && paths == 1
	}
	b, err := readSmall(filepath.Join(env, "uv-receipt.toml"))
	if err != nil {
		return false
	}
	// Only generated inline entries in [tool] are accepted; unknown layouts keep manual ownership.
	text := strings.TrimSpace(string(b))
	if !strings.HasPrefix(text, "[tool]\n") || strings.Count(text, "[tool]") != 1 {
		return false
	}
	text = strings.TrimPrefix(text, "[tool]\n")
	if n := strings.Index(text, "\n["); n >= 0 {
		text = text[:n]
	}
	entries := regexp.MustCompile(`(?ms)^entrypoints\s*=\s*\[([^\]]*)\]`).FindAllSubmatch([]byte(text), -1)
	if len(entries) != 1 {
		return false
	}
	count := 0
	for _, row := range regexp.MustCompile(`\{([^{}]*)\}`).FindAllSubmatch(entries[0][1], -1) {
		fields := map[string]string{}
		valid := true
		for _, field := range strings.Split(string(row[1]), ",") {
			key, value, ok := strings.Cut(field, "=")
			var decoded string
			key = strings.TrimSpace(key)
			if !ok || fields[key] != "" || json.Unmarshal([]byte(strings.TrimSpace(value)), &decoded) != nil {
				valid = false
				break
			}
			fields[key] = decoded
		}
		if valid && fields["name"] == name && fields["install-path"] == filepath.Join(bin, name) && (fields["from"] == "" || normalize(fields["from"]) == normalize(candidate.Identity)) {
			count++
		}
	}
	return count == 1
}

func pythonEntryUnmodified(dist, entry, env string) bool {
	data, err := readSmall(filepath.Join(dist, "RECORD"))
	if err != nil {
		return false
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		return false
	}
	count := 0
	for _, row := range rows {
		if len(row) != 3 {
			return false
		}
		path := filepath.Clean(filepath.Join(filepath.Dir(dist), filepath.FromSlash(row[0])))
		rel, _ := filepath.Rel(env, path)
		if !within(path, env) || real(path) != filepath.Join(real(env), rel) && !strings.HasSuffix(path, ".pyc") {
			return false
		}
		if row[1] == "" && row[2] == "" && (path == filepath.Join(dist, "RECORD") || strings.HasSuffix(path, ".pyc")) {
			continue
		}
		payload, err := readSmall(path)
		if err != nil {
			return false
		}
		sum := sha256.Sum256(payload)
		hash := "sha256=" + base64.RawURLEncoding.EncodeToString(sum[:])
		if row[1] != hash || row[2] != strconv.Itoa(len(payload)) {
			return false
		}
		if path == entry {
			count++
		}
	}
	return count == 1
}
