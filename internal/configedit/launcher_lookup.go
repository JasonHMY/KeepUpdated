package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

var errNPMLookup = errors.New("mcp_launcher_lookup_requires_review")

// npx may satisfy an exact registry spec from a local, global or execution-cache
// tree by package identity alone. Only an absent local tree and empty execution
// cache are supported here; global aliases are checked by manifest name.
func npmLookupIdentity(cwd, home, prefix, name string) (string, error) {
	if !staticAbsolute(prefix) {
		return "", errNPMLookup
	}
	var evidence bytes.Buffer
	absent := func(path string) error {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errNPMLookup
		}
		fmt.Fprintf(&evidence, "absent %q\n", path)
		return nil
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, entry := range []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "node_modules"} {
			if err := absent(filepath.Join(dir, entry)); err != nil {
				return "", err
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	cache := filepath.Join(home, ".npm", "_npx")
	cached, err := npmLookupDirectory(cache, &evidence)
	if err != nil || len(cached) != 0 {
		return "", errNPMLookup
	}

	// npm exec supplies dirname(globalDir) as Arborist's global root.
	root := filepath.Join(prefix, "lib")
	for _, entry := range []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "node_modules/.package-lock.json"} {
		if err := absent(filepath.Join(root, entry)); err != nil {
			return "", err
		}
	}
	global := filepath.Join(root, "node_modules")
	entries, err := npmLookupDirectory(global, &evidence)
	if err != nil {
		return "", err
	}
	manifests, bytesRead := 0, 0
	inspect := func(path string) error {
		manifests++
		if manifests > 512 {
			return errNPMLookup
		}
		manifest := filepath.Join(path, "package.json")
		real, err := filepath.EvalSymlinks(manifest)
		if err != nil {
			return errNPMLookup
		}
		data, _, _, err := readConfig(real)
		if err != nil {
			return errNPMLookup
		}
		after, err := filepath.EvalSymlinks(manifest)
		if err != nil || after != real {
			return errNPMLookup
		}
		bytesRead += len(data)
		if bytesRead > 8<<20 {
			return errNPMLookup
		}
		var pkg struct{ Name string }
		if json.Unmarshal(data, &pkg) != nil || pkg.Name == "" || pkg.Name == name {
			return errNPMLookup
		}
		fmt.Fprintf(&evidence, "package %q %q %s\n", path, real, digest(data))
		return nil
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(global, entry.Name())
		if strings.HasPrefix(entry.Name(), "@") {
			scoped, err := npmLookupDirectory(path, &evidence)
			if err != nil {
				return "", err
			}
			for _, pkg := range scoped {
				if err := inspect(filepath.Join(path, pkg.Name())); err != nil {
					return "", err
				}
			}
		} else if err := inspect(path); err != nil {
			return "", err
		}
	}
	// The early command lookup also accepts a literal name or name@version bin,
	// even without a corresponding package tree. Check all exact-version spellings
	// so discovering the version does not change the stable source identity.
	bins, err := npmLookupDirectory(filepath.Join(prefix, "bin", filepath.Dir(name)), &evidence)
	if err != nil {
		return "", err
	}
	base := filepath.Base(name)
	for _, bin := range bins {
		if bin.Name() == base || strings.HasPrefix(bin.Name(), base+"@") {
			return "", errNPMLookup
		}
	}
	return digest(evidence.Bytes()), nil
}

// Bound reads keep large or uninspectable global trees manual. No package code,
// symlink target code or cache is executed, and only a digest leaves this reader.
func npmLookupDirectory(path string, evidence *bytes.Buffer) ([]os.DirEntry, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		fmt.Fprintf(evidence, "absent %q\n", path)
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errNPMLookup
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return nil, errNPMLookup
	}
	entries, err := file.ReadDir(513)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 512 {
		return nil, errNPMLookup
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(info, current) {
		return nil, errNPMLookup
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, errNPMLookup
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	fmt.Fprintf(evidence, "directory %q %q\n", path, real)
	for _, entry := range entries {
		fmt.Fprintf(evidence, "entry %q %d\n", entry.Name(), entry.Type())
	}
	return entries, nil
}

func npmBuiltinPrefix(data []byte) string {
	prefix := ""
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "prefix" {
			if prefix != "" {
				return ""
			}
			prefix = strings.TrimSpace(parts[1])
		}
	}
	return prefix
}
