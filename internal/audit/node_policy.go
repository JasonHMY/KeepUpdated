package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// NodeSelector records a declaration, never the installed or lockfile version.
// Unsupported declarations are not serialized: they can contain private URLs.
type NodeSelector struct {
	Selector string `json:"selector"`
	Source   string `json:"source"`
	Digest   string `json:"digest"`
}

// ReadNodeSelector reads the owning global manifest or npm's retained request.
// npm's absent request is a default policy, not evidence that no pin ever existed.
func ReadNodeSelector(owner, name, path, scope string) (NodeSelector, error) {
	failed := NodeSelector{}
	invalid := errors.New("node selector requires review")
	manifestPath := filepath.Join(scope, "package.json")
	if owner == "npm" {
		manifestPath = filepath.Join(scope, "lib/package.json")
	}
	manifest, err := readSmall(manifestPath)
	selector, source := "", "global-manifest"
	if err == nil {
		m, e := nodeObject(manifest)
		if e != nil {
			return failed, invalid
		}
		// These fields can select another manager, runtime, workspace or resolver.
		for _, key := range []string{"packageManager", "devEngines", "pnpm", "workspaces", "overrides", "resolutions"} {
			if _, ok := m[key]; ok {
				return failed, invalid
			}
		}
		count := 0
		for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
			raw, ok := m[key]
			if !ok {
				continue
			}
			deps, e := nodeObject(raw)
			if e != nil {
				return failed, invalid
			}
			if raw, ok := deps[name]; ok {
				if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &selector) != nil {
					return failed, invalid
				}
				count++
			}
		}
		if count != 1 {
			return failed, invalid
		}
	} else if owner != "npm" || !os.IsNotExist(err) {
		return failed, invalid
	}
	if owner == "npm" {
		data, e := readSmall(filepath.Join(path, "package.json"))
		if e != nil {
			return failed, invalid
		}
		m, e := nodeObject(data)
		if e != nil {
			return failed, invalid
		}
		// Older npm request records have differing semantics. Do not mistake an
		// exact resolution, _from, or an opaque request object for a durable pin.
		for _, key := range []string{"_requested", "_from", "_spec"} {
			if _, ok := m[key]; ok {
				return failed, invalid
			}
		}
		if os.IsNotExist(err) {
			selector = "latest"
			source = "npm-default-latest"
			manifest = []byte("absent")
		}
	}
	if !NodeSelectorSupported(selector) {
		return failed, invalid
	}
	digest := sha256.Sum256(append([]byte(source+"\x00"+manifestPath+"\x00"), manifest...))
	return NodeSelector{Selector: selector, Source: source, Digest: hex.EncodeToString(digest[:])}, nil
}

// StableNodeVersion deliberately accepts only three canonical numeric components.
func StableNodeVersion(s string) ([3]uint64, bool) {
	var v [3]uint64
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for n, p := range parts {
		if p == "" || len(p) > 1 && p[0] == '0' {
			return v, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return v, false
			}
		}
		x, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return v, false
		}
		v[n] = x
	}
	return v, true
}
func NodeSelectorSupported(s string) bool {
	if s == "" || s == "*" || s == "latest" {
		return true
	}
	if s[0] == '^' || s[0] == '~' {
		s = s[1:]
	}
	_, ok := StableNodeVersion(s)
	return ok
}
func NodeSelectorAllows(selector, target string) bool {
	v, ok := StableNodeVersion(target)
	if !ok || !NodeSelectorSupported(selector) {
		return false
	}
	if selector == "" || selector == "*" || selector == "latest" {
		return true
	}
	op := byte('=')
	if selector[0] == '^' || selector[0] == '~' {
		op = selector[0]
		selector = selector[1:]
	}
	base, _ := StableNodeVersion(selector)
	if op == '=' {
		return v == base
	}
	for n := range v {
		if v[n] < base[n] {
			return false
		}
		if v[n] > base[n] {
			break
		}
	}
	if op == '~' {
		return v[0] == base[0] && v[1] == base[1]
	}
	if base[0] > 0 {
		return v[0] == base[0]
	}
	if base[1] > 0 {
		return v[0] == 0 && v[1] == base[1]
	}
	return v == base
}

// Duplicate declaration keys are ambiguous even when encoding/json would keep
// the last value. Only the manifest and dependency objects need this check.
func nodeObject(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid node declaration")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid node declaration")
		}
		if _, ok := out[key]; ok {
			return nil, errors.New("duplicate node declaration")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing node declaration")
	}
	return out, nil
}
