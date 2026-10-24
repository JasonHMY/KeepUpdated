package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var nativeVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// UVReceipt is the narrow standalone cargo-dist receipt supported by this adapter.
// Unknown fields fail closed so a future pin or source override is not discarded.
type UVReceipt struct {
	Binaries      []string            `json:"binaries"`
	BinaryAliases map[string][]string `json:"binary_aliases"`
	CDylibs       []string            `json:"cdylibs"`
	CStaticlibs   []string            `json:"cstaticlibs"`
	InstallLayout string              `json:"install_layout"`
	InstallPrefix string              `json:"install_prefix"`
	ModifyPath    bool                `json:"modify_path"`
	Provider      struct {
		Source  string `json:"source"`
		Version string `json:"version"`
	} `json:"provider"`
	Source struct {
		AppName     string `json:"app_name"`
		Name        string `json:"name"`
		Owner       string `json:"owner"`
		ReleaseType string `json:"release_type"`
	} `json:"source"`
	Version string `json:"version"`
}

func UVReceiptPath(home string) string { return filepath.Join(home, ".config/uv/uv-receipt.json") }

// ReadUVReceipt reads metadata only; it never starts an executable.
func ReadUVReceipt(home, binary string) (UVReceipt, error) {
	var r UVReceipt
	prefix := filepath.Join(home, ".local/bin")
	if binary != filepath.Join(prefix, "uv") {
		return r, errors.New("native_cli_prefix_unsupported")
	}
	data, err := readSmall(UVReceiptPath(home))
	if err != nil {
		return r, errors.New("native_cli_receipt_unavailable")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.Decode(new(any)) != io.EOF {
		return r, errors.New("native_cli_receipt_unsupported")
	}
	sort.Strings(r.Binaries)
	if r.InstallPrefix != prefix || r.InstallLayout != "flat" || r.Provider.Source != "cargo-dist" || r.Source.AppName != "uv" || r.Source.Name != "uv" || r.Source.Owner != "astral-sh" || r.Source.ReleaseType != "github" || !nativeVersion.MatchString(r.Version) || len(r.CDylibs) != 0 || len(r.CStaticlibs) != 0 || len(r.BinaryAliases) != 0 || len(r.Binaries) != 2 || r.Binaries[0] != "uv" || r.Binaries[1] != "uvx" {
		return r, errors.New("native_cli_source_or_layout_unverified")
	}
	for _, path := range []string{binary, filepath.Join(prefix, "uvx"), UVReceiptPath(home)} {
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() {
			return r, errors.New("native_cli_file_unverified")
		}
		rp, err := filepath.EvalSymlinks(path)
		if err != nil || rp != path {
			return r, errors.New("native_cli_linked_installation")
		}
	}
	return r, nil
}

func (s *Scanner) nativeCLI(i *Item) {
	if i.Name != "uv" || i.Source != "standalone" {
		return
	}
	r, err := ReadUVReceipt(s.Config.Home, i.Path)
	if err != nil {
		return
	}
	i.Owner, i.Source, i.Identity, i.Version, i.Channel = "uv-standalone", "github:astral-sh/uv", "uv", r.Version, "stable"
	i.Evidence = append(i.Evidence, "standalone receipt; binary provenance requires update preview")
}
