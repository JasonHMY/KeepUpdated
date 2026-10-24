package audit

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
)

// Product identity is a bundle ID, never a display-name search.
func (s *Scanner) vendorSource(i *Item) {
	if i.Owner == "app-component" || i.Source != "unknown" {
		return
	}
	switch i.Identity {
	case "com.google.Chrome":
		i.Source = "vendor:chrome"
		i.Channel = "stable"
	case "com.microsoft.VSCode", "com.todesktop.230313mzl4w4u92":
		var p struct {
			Quality string `json:"quality"`
		}
		b, e := readSmall(filepath.Join(i.Path, "Contents/Resources/app/product.json"))
		if e != nil || json.Unmarshal(b, &p) != nil || p.Quality != "stable" {
			i.Update = result("unknown", "vendor_channel_unresolved")
			return
		}
		i.Source = "vendor:vscode"
		if i.Identity == "com.todesktop.230313mzl4w4u92" {
			i.Source = "vendor:cursor"
		}
		i.Channel = "stable"
	case "md.obsidian":
		i.Source = "vendor:obsidian"
		i.Channel = "stable"
		for _, p := range glob(filepath.Join(s.Config.Home, "Library/Application Support/obsidian/obsidian-*.asar")) {
			v := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "obsidian-"), ".asar")
			if numeric.MatchString(v) {
				i.payloadVersions = append(i.payloadVersions, v)
				i.Evidence = append(i.Evidence, "cached-payload:"+v)
			}
		}
	case "org.mozilla.firefox":
		b, e := readSmall(filepath.Join(i.Path, "Contents/Resources/defaults/pref/channel-prefs.js"))
		if e != nil || !strings.Contains(string(b), `pref("app.update.channel", "release")`) {
			i.Update = result("unknown", "vendor_channel_unresolved")
			return
		}
		i.Source = "vendor:firefox"
		i.Channel = "stable"
	default:
		if !s.commonVendorSource(i) {
			s.catalogVendorSource(i)
		}
		return
	}
	i.lookup = i.Identity
	i.Update = result("unknown", "online_query_required")
}
func appArch(i *Item, host string) string {
	if host == "amd64" {
		host = "x86_64"
	}
	for _, a := range i.Architecture {
		if a == host {
			return a
		}
	}
	if len(i.Architecture) == 1 && (i.Architecture[0] == "arm64" || i.Architecture[0] == "x86_64") {
		return i.Architecture[0]
	}
	return ""
}
func (s *Scanner) checkVendor(i *Item) {
	if s.checkCommonVendor(i) || s.checkCatalogVendor(i) {
		return
	}
	latest, code := "", ""
	arch := appArch(i, s.Report.Architecture)
	switch i.Source {
	case "vendor:chrome":
		if arch == "" {
			i.Update = result("unknown", "vendor_architecture_unresolved")
			return
		}
		platform := "mac"
		if arch == "arm64" {
			platform = "mac_arm64"
		}
		var m struct {
			Versions []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"versions"`
		}
		prefix := "chrome/platforms/" + platform + "/channels/stable/versions/"
		code = s.get("https://versionhistory.googleapis.com/v1/"+prefix+"?pageSize=1&order_by="+url.QueryEscape("version desc"), &m)
		if code == "" {
			if len(m.Versions) != 1 || m.Versions[0].Name != prefix+m.Versions[0].Version {
				code = "identity_mismatch"
			} else {
				latest = m.Versions[0].Version
			}
		}
	case "vendor:vscode", "vendor:cursor":
		if arch == "" {
			i.Update = result("unknown", "vendor_architecture_unresolved")
			return
		}
		platform := "darwin"
		if arch == "arm64" {
			platform = "darwin-arm64"
		}
		endpoint := "https://update.code.visualstudio.com/api/update/" + platform + "/stable/latest"
		if i.Source == "vendor:cursor" {
			if arch == "x86_64" {
				platform = "darwin-x64"
			}
			endpoint = "https://api2.cursor.sh/updates/api/update/" + platform + "/cursor/0.0.0/stable"
		}
		var m struct {
			ProductVersion string `json:"productVersion"`
			Name           string `json:"name"`
		}
		code = s.get(endpoint, &m)
		latest = m.ProductVersion
		if latest == "" {
			latest = m.Name
		}
	case "vendor:obsidian":
		var m struct {
			Latest  string `json:"latestVersion"`
			Minimum string `json:"minimumVersion"`
		}
		code = s.get("https://raw.githubusercontent.com/obsidianmd/obsidian-releases/master/desktop-releases.json", &m)
		latest = m.Latest
		if code == "" {
			if len(i.payloadVersions) > 0 {
				i.Latest = latest
				i.Update = result("unknown", "application_payload_activity_unknown")
				return
			}
			if cmp, ok := compare(i.Version, m.Minimum); !ok || cmp < 0 {
				code = "vendor_minimum_version_unresolved"
			}
		}
	case "vendor:firefox":
		var m map[string]string
		code = s.get("https://product-details.mozilla.org/1.0/firefox_versions.json", &m)
		latest = m["LATEST_FIREFOX_VERSION"]
	default:
		i.Update = result("unsupported", "vendor_adapter_missing")
		return
	}
	if code != "" {
		status := "error"
		if code == "identity_mismatch" || code == "vendor_minimum_version_unresolved" {
			status = "unknown"
		}
		i.Update = result(status, code)
		return
	}
	i.Latest = latest
	if latest == "" {
		i.Update = result("unknown", "missing_latest_version")
		return
	}
	i.Evidence = append(i.Evidence, "vendor-identity:exact-bundle", "vendor-channel:stable")
	setCompared(i, i.Version, latest)
}
