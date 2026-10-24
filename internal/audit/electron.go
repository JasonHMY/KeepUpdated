package audit

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

type electronProduct struct{ base, channel string }

var electronProducts = map[string]electronProduct{
	"notion.id":             {"https://desktop-release.notion-static.com/", "arm64"},
	"now.typeless.desktop":  {"https://typeless-static.com/desktop-release/", "arm64"},
	"com.moonshot.kimichat": {"https://kimi-img.moonshot.cn/app/upgrade/", "latest"},
	"ai.today.macos.app":    {"https://releases.today.ai/apps/today/desktop/macos/release/arm64/", "latest"},
}

var electronVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var electronARM = regexp.MustCompile(`(^|[-_.])arm64($|[-_.])`)
var electronIntel = regexp.MustCompile(`(^|[-_.])(x64|x86_64)($|[-_.])`)
var electronUniversal = regexp.MustCompile(`(^|[-_.])universal($|[-_.])`)

func (s *Scanner) electronSource(i *Item) {
	product, known := electronProducts[i.Identity]
	if !known || i.Kind != "application" || i.Owner == "app-component" || i.Source != "unknown" {
		return
	}
	b, err := readSmall(filepath.Join(i.Path, "Contents/Resources/app-update.yml"))
	if err != nil {
		return
	}
	config, ok := electronConfig(string(b))
	if !ok {
		i.Update = result("unknown", "electron_configuration_unresolved")
		return
	}
	if config["provider"] != "generic" {
		return
	}
	channel := config["channel"]
	if channel == "" {
		channel = "latest"
	}
	base, err := url.Parse(config["url"])
	if err != nil || !publicURL(config["url"], true) {
		i.Update = result("unknown", "feed_requires_unsafe_or_authenticated_request")
		return
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/"
	if base.String() != product.base {
		i.Update = result("unknown", "electron_source_unverified")
		return
	}
	if channel != product.channel {
		i.Update = result("unknown", "electron_channel_unresolved")
		return
	}
	arch := appArch(i, s.Report.Architecture)
	if arch == "" || channel == "arm64" && arch != "arm64" {
		i.Update = result("unknown", "vendor_architecture_unresolved")
		return
	}
	i.Source, i.Channel = "electron-generic", channel
	i.lookup = base.ResolveReference(&url.URL{Path: channel + "-mac.yml"}).String()
	i.Evidence = append(i.Evidence, "Contents/Resources/app-update.yml", "electron-identity:exact-bundle", "electron-channel:static-mac")
	i.Update = result("unknown", "online_query_required")
}

// shortcut: only verified products and flat public generic configs are supported; extend before accepting other provider options.
func electronConfig(text string) (map[string]string, bool) {
	config := map[string]string{}
	for _, raw := range strings.Split(text, "\n") {
		line := yamlSourceLine(strings.TrimSpace(raw))
		if line == "" {
			continue
		}
		if raw != strings.TrimLeft(raw, " \t") {
			return nil, false
		}
		key, value, ok := yamlSourcePair(line)
		if !ok {
			return nil, false
		}
		if _, duplicate := config[key]; duplicate {
			return nil, false
		}
		switch key {
		case "provider", "url", "channel", "updaterCacheDirName", "useMultipleRangeRequest":
		default:
			return nil, false
		}
		config[key], ok = yamlSourceScalar(value)
		if !ok {
			return nil, false
		}
	}
	return config, true
}

func (s *Scanner) checkElectron(i *Item) {
	b, code := s.feedBytes(i.lookup)
	if code != "" {
		status := "error"
		if code == "feed_requires_unsafe_or_authenticated_request" {
			status = "unknown"
		}
		i.Update = result(status, code)
		return
	}
	manifest, code := parseElectronManifest(string(b))
	if code != "" {
		i.Update = result("unknown", code)
		return
	}
	i.Latest = manifest.version
	arch := appArch(i, s.Report.Architecture)
	compatible := false
	for _, file := range manifest.files {
		u, err := url.Parse(file)
		if err != nil {
			continue
		}
		name := strings.ToLower(filepath.Base(u.Path))
		if !strings.HasSuffix(name, ".zip") {
			continue
		}
		arm, intel, universal := electronARM.MatchString(name), electronIntel.MatchString(name), electronUniversal.MatchString(name)
		if arm && intel || universal && (arm || intel) {
			i.Update = result("unknown", "electron_architecture_unresolved")
			return
		}
		compatible = compatible || universal && (arch == "arm64" || arch == "x86_64") || arch == "arm64" && arm || arch == "x86_64" && intel
	}
	if !compatible {
		i.Update = result("unknown", "electron_architecture_unresolved")
		return
	}
	if !electronVersion.MatchString(i.Version) || !electronVersion.MatchString(manifest.version) {
		i.Update = result("unknown", "version_scheme_or_active_version_unresolved")
		return
	}
	i.Evidence = append(i.Evidence, "Electron static macOS channel metadata", "feed-architecture:matching-zip")
	setCompared(i, i.Version, manifest.version)
}

type electronManifest struct {
	version string
	files   []string
}

// shortcut: accept the documented block-map update subset; unknown constraints stay unresolved instead of guessing YAML semantics.
func parseElectronManifest(text string) (electronManifest, string) {
	m := electronManifest{}
	seen := map[string]bool{}
	section := ""
	fileFields := map[string]bool{}
	inFile := false
	for _, raw := range strings.Split(text, "\n") {
		line := yamlSourceLine(strings.TrimSpace(raw))
		if line == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if strings.Contains(raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))], "\t") {
			return m, "electron_metadata_unresolved"
		}
		if indent > 0 && section == "releaseNotes" {
			continue
		}
		if indent == 0 {
			key, value, ok := yamlSourcePair(line)
			if !ok || seen[key] {
				return m, "electron_metadata_unresolved"
			}
			seen[key], section = true, key
			inFile = false
			switch key {
			case "files":
				if value != "" {
					return m, "electron_metadata_unresolved"
				}
				continue
			case "releaseNotes":
				if value == "|" || value == "|-" || value == "|+" || value == ">" || value == ">-" || value == ">+" {
					continue
				}
			case "version", "path", "sha512", "sha2", "releaseDate", "releaseName", "build":
			default:
				// Electron compares minimumSystemVersion against Darwin's kernel release, not sw_vers.
				return m, "electron_constraints_unresolved"
			}
			scalar, ok := yamlSourceScalar(value)
			if !ok {
				return m, "electron_metadata_unresolved"
			}
			if key == "version" {
				m.version = scalar
			}
			if key == "path" {
				m.files = append(m.files, scalar)
			}
			continue
		}
		if section != "files" || indent != 2 && indent != 4 {
			return m, "electron_metadata_unresolved"
		}
		if indent == 2 {
			if !strings.HasPrefix(line, "- ") {
				return m, "electron_metadata_unresolved"
			}
			line, fileFields = strings.TrimSpace(line[2:]), map[string]bool{}
			inFile = true
		} else if !inFile {
			return m, "electron_metadata_unresolved"
		}
		key, value, ok := yamlSourcePair(line)
		if !ok || fileFields[key] {
			return m, "electron_metadata_unresolved"
		}
		fileFields[key] = true
		switch key {
		case "url", "sha512", "sha2", "size", "blockMapSize":
		default:
			return m, "electron_constraints_unresolved"
		}
		scalar, ok := yamlSourceScalar(value)
		if !ok {
			return m, "electron_metadata_unresolved"
		}
		if key == "url" {
			m.files = append(m.files, scalar)
		}
	}
	if m.version == "" || len(m.files) == 0 {
		return m, "electron_metadata_unresolved"
	}
	return m, ""
}
