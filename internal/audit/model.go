package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const Schema = "keepupdated/v1"

type Result struct {
	Status string `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// AppStoreProduct is navigation metadata, never installation authorization.
type AppStoreProduct struct {
	ID       int64  `json:"id"`
	BundleID string `json:"bundle_id"`
	Country  string `json:"country"`
}
type Item struct {
	NodeSelector  *NodeSelector    `json:"node_selector,omitempty"`
	PluginCache   *PluginCache     `json:"plugin_cache,omitempty"`
	AppStore      *AppStoreProduct `json:"app_store,omitempty"`
	Plugin        *PluginOwner     `json:"plugin,omitempty"`
	ID            string           `json:"id"`
	Kind          string           `json:"kind"`
	Name          string           `json:"name"`
	Path          string           `json:"path"`
	RealPath      string           `json:"real_path,omitempty"`
	Owner         string           `json:"owner"`
	Identity      string           `json:"identity,omitempty"`
	Version       string           `json:"installed_version,omitempty"`
	Build         string           `json:"installed_build,omitempty"`
	LatestBuild   string           `json:"latest_build,omitempty"`
	ActiveVersion string           `json:"active_version,omitempty"`
	Versions      []string         `json:"installed_versions,omitempty"`
	Active        bool             `json:"active"`
	ActivityKnown bool             `json:"activity_known"`
	Architecture  []string         `json:"architecture,omitempty"`
	Source        string           `json:"source"`
	Channel       string           `json:"channel"`
	Latest        string           `json:"latest_version,omitempty"`
	Evidence      []string         `json:"evidence"`
	Health        Result           `json:"health"`
	Update        Result           `json:"update"`
	Related       []string         `json:"related,omitempty"`
	Dependencies  []Dependency     `json:"dependencies,omitempty"`

	SkillManager *SkillManagerSource `json:"skill_manager,omitempty"`

	// Private lookup data is never serialized, including configuration URLs and arguments.
	lookup          string
	mcpSearchPath   string
	payloadVersions []string
	pythonSite      string
	pythonEnv       string
	pythonRequires  []string
	skillSource     *skillSource
	requiresPython  string
}
type Coverage struct {
	Adapter string `json:"adapter"`
	Level   string `json:"level"`
	Detail  string `json:"detail"`
}
type Diagnostic struct {
	Adapter string `json:"adapter"`
	Path    string `json:"path,omitempty"`
	Code    string `json:"code"`
}
type Report struct {
	ToolVersion  string         `json:"tool_version,omitempty"`
	SourceCommit string         `json:"source_commit,omitempty"`
	Schema       string         `json:"schema"`
	Command      string         `json:"command"`
	GeneratedAt  string         `json:"generated_at"`
	OS           string         `json:"os"`
	Architecture string         `json:"architecture"`
	Online       bool           `json:"online"`
	Items        []Item         `json:"items"`
	Summary      map[string]int `json:"summary"`
	Diagnostics  []Diagnostic   `json:"diagnostics"`
	Coverage     []Coverage     `json:"coverage"`
	Queries      []string       `json:"queries"`
}

func result(status, code string) Result { return Result{Status: status, Code: code} }
func item(kind, name, path, owner string) Item {
	sum := sha256.Sum256([]byte(kind + "\x00" + path))
	return Item{ID: hex.EncodeToString(sum[:12]), Kind: kind, Name: name, Path: path, Owner: owner, Source: "unknown", Channel: "unknown", Evidence: []string{}, Health: result("unknown", "not_checked"), Update: result("unknown", "no_update_source")}
}
func (s *Scanner) add(i Item) {
	if s.seen[i.ID] {
		return
	}
	s.seen[i.ID] = true
	if i.Path != "" {
		if p, err := filepath.EvalSymlinks(i.Path); err == nil {
			i.RealPath = p
		}
	}
	s.Report.Items = append(s.Report.Items, i)
}
func (s *Scanner) diag(adapter, path, code string) {
	s.Report.Diagnostics = append(s.Report.Diagnostics, Diagnostic{adapter, path, code})
}
func (s *Scanner) finish() {
	for n := range s.Report.Items {
		i := &s.Report.Items[n]
		if i.lookup != "" && i.Update.Status == "unknown" && i.Update.Code == "no_update_source" {
			i.Update = result("unknown", "online_query_required")
		}
	}
	sort.Slice(s.Report.Items, func(a, b int) bool { return s.Report.Items[a].ID < s.Report.Items[b].ID })
	s.Report.Summary = map[string]int{"total": len(s.Report.Items)}
	for _, i := range s.Report.Items {
		s.Report.Summary[i.Update.Status]++
	}
}
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return f, nil
}
func readSmall(path string) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > 4<<20 {
		return nil, os.ErrInvalid
	}
	b := make([]byte, st.Size())
	_, err = f.ReadAt(b, 0)
	if len(b) == 0 {
		err = nil
	}
	return b, err
}
func exists(p string) bool { _, e := os.Stat(p); return e == nil }
func real(p string) string {
	r, e := filepath.EvalSymlinks(p)
	if e != nil {
		return ""
	}
	return r
}
func within(p, root string) bool {
	rel, e := filepath.Rel(root, p)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func now() string { return time.Now().UTC().Format(time.RFC3339) }
