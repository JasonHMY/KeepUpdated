package manage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// Keep the console-script/RECORD ownership proof used for MCP aliases. Script
// bytes may change on update; their preview hash belongs to Binding, not policy.
func pythonEntrypointHash(i audit.Item, env string, entries []PythonEntrypoint, managerVersion string) (string, error) {
	var bound strings.Builder
	for _, entry := range entries {
		declared := filepath.Join(env, "bin", entry.Name)
		if ownedPath(declared, true) != nil || ownedPath(entry.Path, true) != nil {
			return "", errors.New("entrypoint permission unverified")
		}
		realEnv, e1 := filepath.EvalSymlinks(env)
		realEntry, e2 := filepath.EvalSymlinks(entry.Path)
		st, e3 := os.Lstat(declared)
		if e1 != nil || e2 != nil || e3 != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 || realEntry != filepath.Join(realEnv, "bin", entry.Name) {
			return "", errors.New("entrypoint exposure changed")
		}
		meta, err := readMetadata(filepath.Join(i.Path, "entry_points.txt"))
		if err != nil {
			return "", err
		}
		section := ""
		matches := 0
		for _, line := range strings.Split(string(meta), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = line
				continue
			}
			if section == "[console_scripts]" {
				key, value, ok := strings.Cut(line, "=")
				if ok && strings.TrimSpace(key) == entry.Name && strings.TrimSpace(value) != "" {
					matches++
				}
			}
		}
		if matches != 1 {
			return "", errors.New("entrypoint declaration unavailable")
		}
		record, err := readMetadata(filepath.Join(i.Path, "RECORD"))
		if err != nil {
			return "", err
		}
		rows, err := csv.NewReader(strings.NewReader(string(record))).ReadAll()
		if err != nil {
			return "", err
		}
		payload, err := readMetadata(declared)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(payload)
		hash := "sha256=" + base64.RawURLEncoding.EncodeToString(sum[:])
		matches = 0
		for _, row := range rows {
			if len(row) != 3 {
				return "", errors.New("invalid wheel record")
			}
			if filepath.Clean(filepath.Join(filepath.Dir(i.Path), filepath.FromSlash(row[0]))) == declared {
				if !pythonScriptRecordMatches(payload, row, env, i.Owner, managerVersion) {
					return "", errors.New("entrypoint content changed")
				}
				matches++
			}
		}
		if matches != 1 {
			return "", errors.New("entrypoint wheel ownership unavailable")
		}
		bound.WriteString(entry.Path + "\x00" + realEntry + "\x00" + hash + "\x00" + hashText(string(meta)) + "\x00" + hashText(string(record)))
	}
	return hashText(bound.String()), nil
}
func pythonEntrypointsRemoved(p PlanItem) bool {
	if p.Binding.Python == nil {
		return false
	}
	for _, entry := range p.Binding.Python.Entrypoints {
		if _, err := os.Lstat(entry.Path); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func pythonScriptRecordMatches(payload []byte, row []string, env, owner, managerVersion string) bool {
	matches := func(data []byte) bool {
		sum := sha256.Sum256(data)
		return row[1] == "sha256="+base64.RawURLEncoding.EncodeToString(sum[:]) && row[2] == strconv.Itoa(len(data))
	}
	if matches(payload) {
		return true
	}
	// pipx 1.18.4 adds -E while exposing a Python launcher without updating RECORD.
	// Only reverse that exact, version-bound shebang transform; retain the full hash.
	prefix := "#!" + filepath.Join(env, "bin/python") + " -E\n"
	if owner != "pipx" || managerVersion != "1.18.4" || !strings.HasPrefix(string(payload), prefix) {
		return false
	}
	original := []byte("#!" + filepath.Join(env, "bin/python") + "\n" + strings.TrimPrefix(string(payload), prefix))
	return matches(original)
}
