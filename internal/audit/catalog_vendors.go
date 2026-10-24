package audit

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Public feeds are bound to exact products. No display-name or cask-name guessing.
func (s *Scanner) catalogVendorSource(i *Item) {
	if i.Owner == "app-component" || i.Source != "unknown" {
		return
	}
	feed := ""
	switch i.Identity {
	case "com.openai.codex":
		arch := appArch(i, s.Report.Architecture)
		if arch == "" {
			i.Update = result("unknown", "vendor_architecture_unresolved")
			return
		}
		feed = "https://persistent.oaistatic.com/codex-app-prod/appcast.xml"
		if arch == "x86_64" {
			feed = "https://persistent.oaistatic.com/codex-app-prod/appcast-x64.xml"
		}
	case "com.mitchellh.ghostty":
		feed = "https://release.files.ghostty.org/appcast.xml"
	case "com.tencent.xinWeChat":
		feed = "https://dldir1.qq.com/weixin/mac/mac-release.xml"
	case "com.caldis.Mos":
		feed = "https://mos.caldis.me/appcast.xml"
	case "com.anthropic.claudefordesktop":
		i.Source = "vendor:claude"
	case "com.ccswitch.desktop":
		i.Source = "vendor:cc-switch"
	case "com.electron.ollama":
		i.Source = "vendor:ollama"
	case "app.openbot.desktop":
		i.Source = "vendor:openbot"
	case "net.xmind.vana.app":
		i.Source = "vendor:xmind"
	case "com.jetbrains.intellij", "com.jetbrains.pycharm":
		var info struct{ ProductCode, Version, BuildNumber string }
		b, err := readSmall(filepath.Join(i.Path, "Contents/Resources/product-info.json"))
		if err != nil || json.Unmarshal(b, &info) != nil || info.Version != i.Version || i.Build != info.ProductCode+"-"+info.BuildNumber {
			i.Update = result("unknown", "vendor_product_unresolved")
			return
		}
		if i.Identity == "com.jetbrains.intellij" && info.ProductCode == "IU" {
			i.lookup = "IIU"
		} else if i.Identity == "com.jetbrains.pycharm" && info.ProductCode == "PY" {
			i.lookup = "PCP"
		} else {
			i.Update = result("unknown", "vendor_product_unresolved")
			return
		}
		i.Source = "vendor:jetbrains"
	default:
		return
	}
	if !numeric.MatchString(i.Version) {
		i.Update = result("unknown", "vendor_channel_unresolved")
		i.lookup = ""
		return
	}
	if feed != "" {
		i.Source, i.lookup = "sparkle", feed
	}
	if i.lookup == "" {
		i.lookup = i.Identity
	}
	i.Channel = "public-stable"
	i.Evidence = append(i.Evidence, "vendor-identity:exact-bundle", "vendor-channel:public-stable", "configured-update-channel:not-inspected")
	i.Update = result("unknown", "online_query_required")
}

func (s *Scanner) checkCatalogVendor(i *Item) bool {
	latest, code := "", ""
	installed, compared := i.Version, ""
	switch i.Source {
	case "vendor:claude":
		var m struct {
			Current  string `json:"currentRelease"`
			Releases []struct {
				Version string                        `json:"version"`
				Update  struct{ Version, URL string } `json:"updateTo"`
			} `json:"releases"`
		}
		code = s.get("https://downloads.claude.ai/releases/darwin/universal/RELEASES.json", &m)
		if code == "" {
			for _, r := range m.Releases {
				if r.Version != m.Current || r.Update.Version != m.Current {
					continue
				}
				u, err := url.Parse(r.Update.URL)
				if err == nil && commonURL(r.Update.URL, "downloads.claude.ai", u.Path) && strings.HasPrefix(u.Path, "/releases/darwin/universal/"+m.Current+"/Claude-") && strings.HasSuffix(u.Path, ".zip") {
					latest = m.Current
				}
			}
			if latest == "" {
				code = "identity_mismatch"
			}
		}
	case "vendor:cc-switch", "vendor:ollama", "vendor:openbot":
		repo, asset := "farion1231/cc-switch", ""
		if i.Source == "vendor:ollama" {
			repo, asset = "ollama/ollama", "Ollama-darwin.zip"
		}
		if i.Source == "vendor:openbot" {
			repo = "nightly-labs/openbot"
		}
		var m struct {
			Tag               string `json:"tag_name"`
			Draft, Prerelease bool
			Assets            []struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			} `json:"assets"`
		}
		code = s.get("https://api.github.com/repos/"+repo+"/releases/latest", &m)
		if code == "" {
			latest = strings.TrimPrefix(m.Tag, "v")
			if asset == "" {
				asset = "CC-Switch-v" + latest + "-macOS.dmg"
			}
			if i.Source == "vendor:openbot" {
				arch := appArch(i, s.Report.Architecture)
				if arch == "x86_64" {
					arch = "x64"
				}
				asset = "OpenBot-" + latest + "-" + arch + ".dmg"
			}
			valid := false
			for _, a := range m.Assets {
				if a.Name == asset && commonURL(a.URL, "github.com", "/"+repo+"/releases/download/"+m.Tag+"/"+asset) {
					valid = true
				}
			}
			if m.Draft || m.Prerelease || !valid || !numeric.MatchString(latest) {
				code = "identity_mismatch"
			}
		}
	case "vendor:xmind":
		location, failure := s.commonHeadLocation("https://xmind.app/zen/download/mac/")
		code = failure
		if code == "" {
			u, _ := url.Parse(location)
			match := regexp.MustCompile(`^/Xmind-for-macOS-([0-9]+(?:\.[0-9]+)+)-[0-9]{12}\.dmg$`).FindStringSubmatch(u.Path)
			if match != nil && commonURL(location, "dl3.xmind.app", u.Path) {
				latest = match[1]
			} else {
				code = "identity_mismatch"
			}
		}
	case "vendor:jetbrains":
		if i.lookup != "IIU" && i.lookup != "PCP" {
			code = "identity_mismatch"
			break
		}
		prefix := "IU-"
		if i.lookup == "PCP" {
			prefix = "PY-"
		}
		if !strings.HasPrefix(i.Build, prefix) || !numeric.MatchString(strings.TrimPrefix(i.Build, prefix)) {
			code = "vendor_product_unresolved"
			break
		}
		installed = strings.TrimPrefix(i.Build, prefix)
		var m map[string][]struct {
			Version, Type, Build string
			Downloads            map[string]struct{ Link string }
		}
		code = s.get("https://data.services.jetbrains.com/products/releases?code="+i.lookup+"&latest=true&type=release", &m)
		if code == "" {
			platform := "mac"
			if appArch(i, s.Report.Architecture) == "arm64" {
				platform = "macM1"
			}
			if releases := m[i.lookup]; len(releases) == 1 && releases[0].Type == "release" {
				r := releases[0]
				product := "/idea/idea-"
				if i.lookup == "PCP" {
					product = "/python/pycharm-"
				}
				suffix := ".dmg"
				if platform == "macM1" {
					suffix = "-aarch64.dmg"
				}
				if numeric.MatchString(r.Version) && commonURL(r.Downloads[platform].Link, "download.jetbrains.com", product+r.Version+suffix) && regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)+$`).MatchString(r.Build) {
					latest = r.Version
					i.LatestBuild, compared = r.Build, r.Build
				}
			}
			if latest == "" {
				code = "identity_mismatch"
			}
		}
	default:
		return false
	}
	if appArch(i, s.Report.Architecture) == "" && code == "" {
		code = "vendor_architecture_unresolved"
	}
	if code != "" {
		status := "error"
		if code == "identity_mismatch" || strings.HasPrefix(code, "vendor_") {
			status = "unknown"
		}
		i.Update = result(status, code)
		return true
	}
	i.Latest = latest
	if compared == "" {
		compared = latest
	}
	setCompared(i, installed, compared)
	return true
}
