package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Config struct {
	Progress      func(Progress)
	ManagerRoots  map[string]string
	ManagerBins   map[string]string
	Context       context.Context
	Home          string
	Path          string
	AppRoots      []string
	BrewRoots     []string
	Projects      []string
	MCPConfigs    []string
	SkillRoots    []string
	Online        bool
	SystemUpdates bool
	Timeout       time.Duration
	Country       string
}
type Scanner struct {
	Config       Config
	Report       Report
	seen         map[string]bool
	client       *http.Client
	running      map[string]bool
	processKnown bool
	feedClient   *http.Client
	osVersion    string
}

func DefaultConfig() Config {
	h, _ := os.UserHomeDir()
	roots, bins := ExplicitManagerPaths()
	return Config{ManagerRoots: roots, ManagerBins: bins, Home: h, Path: os.Getenv("PATH"), AppRoots: []string{"/Applications", filepath.Join(h, "Applications")}, BrewRoots: []string{"/opt/homebrew", "/usr/local"}, Timeout: 20 * time.Second, Country: "US"}
}
func New(c Config) *Scanner {
	return &Scanner{Config: c, feedClient: publicClient(c.Timeout), seen: map[string]bool{}, running: map[string]bool{}, client: &http.Client{Timeout: c.Timeout, CheckRedirect: func(r *http.Request, via []*http.Request) error { return errors.New("redirect rejected") }}, Report: Report{Schema: Schema, GeneratedAt: now(), OS: runtime.GOOS, Architecture: runtime.GOARCH, Online: c.Online, Items: []Item{}, Diagnostics: []Diagnostic{}, Queries: []string{}, Coverage: coverage()}}
}
func (s *Scanner) Scan(check bool) Report {
	s.Report.Command = "scan"
	if check {
		s.Report.Command = "check"
	}
	stages := []struct {
		name string
		run  func()
	}{
		{"processes", s.processes}, {"apps", s.apps}, {"homebrew", s.brew},
		{"packages", s.packages}, {"executables", s.executables}, {"runtimes", s.environmentRuntimes},
		{"configurations", s.configs}, {"skills", s.skills}, {"skill-origins", s.skillSources},
		{"skill-transactions", s.skillTransactions}, {"system", s.system}, {"services", s.services}, {"relationships", s.relate},
	}
	progress := s.progress("scan", len(stages))
	for _, stage := range stages {
		if s.cancelled() {
			break
		}
		progress.begin(stage.name)
		before := len(s.Report.Diagnostics)
		stage.run()
		progress.done(len(s.Report.Diagnostics) > before)
	}
	progress.finish()
	if check && !s.cancelled() {
		s.dependencies()
	}
	if check && !s.cancelled() {
		s.checkSkillSources()
	}
	if check && !s.cancelled() {
		s.check()
	}
	if s.cancelled() {
		s.diag("scan", "", "scan_cancelled")
	}
	s.finish()
	return s.Report
}

// CheckManagementDependencies keeps the full local inventory for source and
// consumer guards, inspecting only dependencies needed by the selected operation.
// Call after Scan(false); owner resolvers perform their own candidate checks.
func (s *Scanner) CheckManagementDependencies(include func(Item) bool) Report {
	if !s.cancelled() {
		s.dependenciesWhere(include)
	}
	s.finish()
	return s.Report
}

func (s *Scanner) processes() {
	if runtime.GOOS != "darwin" {
		return
	}
	if b, e := s.run("/bin/ps", "-axo", "comm="); e == nil {
		s.processKnown = true
		for _, p := range strings.Split(string(b), "\n") {
			s.running[strings.TrimSpace(p)] = true
		}
	} else {
		s.diag("apps", "", "process_query_failed")
	}
}

// Commands are selected in code. Never pass configuration commands, args or shell text here.
func (s *Scanner) run(path string, args ...string) ([]byte, error) {
	parent := s.Config.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, s.Config.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + s.Config.Home, "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	var appleMessages limitedBuffer
	appleList := path == "/usr/sbin/softwareupdate" && len(args) == 1 && args[0] == "--list"
	if appleList {
		cmd.Stderr = &appleMessages
	}
	s.Report.Queries = append(s.Report.Queries, filepath.Base(path)+" "+strings.Join(args, " "))
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.overflow || appleMessages.overflow {
		return nil, errors.New("output_limit")
	}
	if appleList {
		data := append([]byte(nil), out.Bytes()...)
		data = append(data, '\n')
		data = append(data, appleMessages.Bytes()...)
		return data, err
	}
	return out.Bytes(), err
}

type limitedBuffer struct {
	// A named field keeps io.Copy from bypassing Write via Buffer.ReadFrom.
	buffer   bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.overflow || len(p) > (8<<20)-b.buffer.Len() {
		b.overflow = true
		return len(p), nil
	}
	return b.buffer.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}
func networkCode(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "query_failed"
}
func (s *Scanner) get(raw string, out any) string {
	b, code := s.getBytes(raw)
	if code != "" {
		return code
	}
	if json.Unmarshal(b, out) != nil {
		return "invalid_response"
	}
	return ""
}
func (s *Scanner) entries(adapter, path string) []os.DirEntry {
	if s.Config.Context != nil && s.Config.Context.Err() != nil {
		return nil
	}
	es, e := os.ReadDir(path)
	if e != nil && !os.IsNotExist(e) {
		s.diag(adapter, path, "unreadable_directory")
	}
	return es
}
func glob(pattern string) []string { p, _ := filepath.Glob(pattern); return p }
func uniq(v []string) []string {
	m := map[string]bool{}
	r := []string{}
	for _, x := range v {
		if x != "" && !m[x] {
			m[x] = true
			r = append(r, x)
		}
	}
	sort.Strings(r)
	return r
}
func coverage() []Coverage {
	return []Coverage{
		{"apps", "partial", "Recursive bundles, identity/version/build and architecture; Sparkle default-channel constraints; exact-ID common vendor public-stable APIs and verified Electron generic feeds. Unknown channel/metadata constraints stay explicit. No payload downloads or dynamic updater state."},
		{"homebrew", "partial", "Cellar receipts, opt/bin links, Caskroom JSON and local taps. Public core/cask API; official alias/rename and absent-definition diagnostics; active receipt dependency presence/source/architecture. Custom tap updates stay unknown."},
		{"app-store", "partial", "Receipt presence; optional public Mac lookup by exact bundle ID and country. No account/receipt decoding or mas integration."},
		{"node-packages", "partial", "Known global roots and PATH-derived prefixes for npm/pnpm/Bun; exact npm registry identity. Static direct/optional/peer dependency resolution and numeric Node engine ranges; no private registry, arbitrary prefixes or script execution."},
		{"python-tools", "partial", "pipx/uv conventional tool environments and explicit project venvs, dist metadata, interpreter links and PEP 668 marker; static same-environment Requires-Dist and numeric constraints/markers; no execution, full PEP 440, extras or arbitrary sys.path reconstruction."},
		{"runtimes-cli", "partial", "PATH and conventional version-manager directories; static Volta v4 images, stored default/package platforms and explicit project pins; version probes only for matching Homebrew core Node/Python/Bun/Go installations, no agent CLI startup."},
		{"mcp", "partial", "Known Codex, Claude, Cursor, VS Code, OpenCode, OpenClaw and Hermes configs plus explicit paths; JSON/JSONC, simple TOML and a conservative YAML subset. Stable executable-to-package ownership where exact; no server startup or health request."},
		{"skills", "partial", "Known Agent/plugin roots and explicit roots; links, entry presence and Git state; existing global/project GitHub source receipts, exact ref/path and live local folder hashes. Verified copied GitHub Skills support confirmed updates with file previews and backups; local changes or unverified sources block replacement. Verified Claude plugin owners support confirmed marketplace refresh/update, exact snapshots, network-isolated execution and explicit reload state; other managed caches remain with their client."},
		{"health", "partial", "PATH and link/runtime diagnostics; launchd definitions, current-user loaded/running state and log sizes without content. System-daemon runtime state, shared library graph and historical log content are not inspected."},
		{"macos", "partial", "sw_vers and architecture; optional explicit softwareupdate --list query, no installation."},
	}
}
