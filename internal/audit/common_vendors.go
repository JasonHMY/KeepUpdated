package audit

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var commonReleaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)
var zoomDisplayVersion = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+) \(([0-9]+)\)$`)
var releaseCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// These adapters compare public stable releases, without inspecting private updater preferences.
func (s *Scanner) commonVendorSource(i *Item) bool {
	if i.Owner == "app-component" || i.Source != "unknown" {
		return false
	}
	switch i.Identity {
	case "com.1password.1password":
		i.Source = "vendor:1password"
	case "us.zoom.xos":
		i.Source = "vendor:zoom"
	case "com.getdropbox.dropbox":
		i.Source = "vendor:dropbox"
	case "com.raycast.macos":
		i.Source = "vendor:raycast"
	case "io.tailscale.ipn.macsys":
		i.Source = "sparkle"
		i.lookup = "https://pkgs.tailscale.com/stable/appcast.xml"
	default:
		return false
	}
	i.Channel = "public-stable"
	i.Evidence = append(i.Evidence, "vendor-identity:exact-bundle", "vendor-channel:public-stable", "configured-update-channel:not-inspected")
	if i.lookup == "" {
		i.lookup = i.Identity
	}
	version := commonInstalledVersion(i)
	if !commonReleaseVersion.MatchString(version) {
		i.lookup = ""
		i.Update = result("unknown", "vendor_channel_unresolved")
		return true
	}
	i.Update = result("unknown", "online_query_required")
	return true
}

func commonInstalledVersion(i *Item) string {
	if i.Identity == "com.getdropbox.dropbox" {
		parts := strings.Split(i.Version, ".")
		if len(parts) != 3 || parts[1] != "4" {
			return ""
		}
	}
	if i.Identity == "io.tailscale.ipn.macsys" {
		parts := strings.Split(i.Version, ".")
		if len(parts) != 3 {
			return ""
		}
		minor, err := strconv.Atoi(parts[1])
		if err != nil || minor%2 != 0 {
			return ""
		}
	}
	if i.Identity == "us.zoom.xos" {
		if m := zoomDisplayVersion.FindStringSubmatch(i.Version); m != nil {
			v := m[1] + "." + m[2]
			if i.Build == v {
				return v
			}
			return ""
		}
		parts := strings.Split(i.Build, ".")
		if len(parts) == 4 && commonReleaseVersion.MatchString(i.Build) && i.Version == strings.Join(parts[:3], ".") {
			return i.Build
		}
		if len(strings.Split(i.Version, ".")) != 4 || i.Build != "" && i.Build != i.Version {
			return ""
		}
	}
	return i.Version
}

func (s *Scanner) checkCommonVendor(i *Item) bool {
	minimum := ""
	switch i.Source {
	case "vendor:1password":
		minimum = "12.0"
	case "vendor:zoom":
		minimum = "10.15"
	case "vendor:dropbox":
		minimum = "10.13"
	case "vendor:raycast":
	default:
		return false
	}
	installed := commonInstalledVersion(i)
	if !commonReleaseVersion.MatchString(installed) {
		i.Update = result("unknown", "vendor_channel_unresolved")
		return true
	}
	arch := appArch(i, s.Report.Architecture)
	if arch == "" {
		i.Update = result("unknown", "vendor_architecture_unresolved")
		return true
	}
	latest, code := "", ""
	switch i.Source {
	case "vendor:1password":
		if !strings.HasPrefix(installed, "8.") {
			code = "vendor_channel_unresolved"
			break
		}
		apiArch := arch
		if arch == "arm64" {
			apiArch = "aarch64"
		}
		var m struct {
			Version string `json:"version"`
			Sources []struct {
				URL string `json:"url"`
			} `json:"sources"`
		}
		code = s.get("https://app-updates.agilebits.com/check/2/99/"+apiArch+"/OPM8/en/0/A1/N", &m)
		latest = m.Version
		if code == "" {
			valid := false
			for _, source := range m.Sources {
				for _, host := range []string{"c.1password.com", "cache.agilebits.com", "cdn.agilebits.com"} {
					if commonURL(source.URL, host, "/dist/1P/mac8/1Password-"+latest+"-"+apiArch+".zip") {
						valid = true
					}
				}
			}
			if !valid || !strings.HasPrefix(latest, "8.") {
				code = "identity_mismatch"
			}
		}
	case "vendor:zoom":
		endpoint := "https://www.zoom.us/client/latest/zoomusInstallerFull.pkg"
		folder := ""
		if arch == "arm64" {
			endpoint += "?archType=arm64"
			folder = "arm64/"
			minimum = "11.0"
		}
		location, failure := s.commonHeadLocation(endpoint)
		code = failure
		if code == "" {
			u, _ := url.Parse(location)
			parts := strings.Split(strings.TrimPrefix(u.Path, "/prod/"), "/")
			if len(parts) > 0 {
				latest = parts[0]
			}
			if !commonURL(location, "cdn.zoom.us", "/prod/"+latest+"/"+folder+"zoomusInstallerFull.pkg") {
				code = "identity_mismatch"
			}
		}
	case "vendor:dropbox":
		endpoint, suffix := "https://www.dropbox.com/download?plat=mac&full=1", ""
		if arch == "arm64" {
			endpoint += "&arch=arm64"
			suffix = ".arm64"
			minimum = "11.0"
		}
		location, failure := s.commonHeadLocation(endpoint)
		code = failure
		if code == "" {
			u, _ := url.Parse(location)
			latest = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/dbx-releng/client/Dropbox "), suffix+".dmg")
			parts := strings.Split(latest, ".")
			if len(parts) != 3 || parts[1] != "4" || !commonURL(location, "edge.dropboxstatic.com", "/dbx-releng/client/Dropbox "+latest+suffix+".dmg") {
				code = "identity_mismatch"
			}
		}
	case "vendor:raycast":
		if strings.HasPrefix(installed, "2.") {
			minimum = "26.0"
			if arch != "arm64" {
				code = "vendor_architecture_unresolved"
				break
			}
			var m struct {
				Version string `json:"version"`
				Commit  string `json:"commit_sha"`
				Builds  []struct {
					Platform, Architecture, Format, URL string
				} `json:"builds"`
			}
			code = s.get("https://x.raycast-releases.com/releases/latest?platform=macos&architecture=arm64", &m)
			latest = m.Version
			if code == "" {
				valid := false
				for _, b := range m.Builds {
					if releaseCommit.MatchString(m.Commit) && b.Platform == "macos" && b.Architecture == arch && b.Format == "dmg" && commonURL(b.URL, "x-r2.raycast-releases.com", "/Raycast_"+latest+"_"+m.Commit[:10]+"_arm64.dmg") {
						valid = true
					}
				}
				if !valid || !strings.HasPrefix(latest, "2.") {
					code = "identity_mismatch"
				}
			}
		} else {
			// shortcut: legacy Raycast OS-specific ceilings need a dedicated feed below macOS 13.
			minimum = "13.0"
			if !strings.HasPrefix(installed, "1.") {
				code = "vendor_channel_unresolved"
				break
			}
			build := arch
			if arch == "arm64" {
				build = "arm"
			}
			var m struct {
				Version string `json:"version"`
			}
			code = s.get("https://releases.raycast.com/releases/latest?build="+build, &m)
			latest = m.Version
			if code == "" && !strings.HasPrefix(latest, "1.") {
				code = "identity_mismatch"
			}
		}
	}
	if code == "" && !commonReleaseVersion.MatchString(latest) {
		code = "invalid_response"
	}
	if code == "" {
		if cmp, known := compare(s.osVersion, minimum); !known || cmp < 0 {
			code = "vendor_minimum_version_unresolved"
		}
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
	setCompared(i, installed, latest)
	return true
}

func commonURL(raw, host, path string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == host && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == path
}

// HEAD only: redirects are metadata and must never turn into a payload request.
func (s *Scanner) commonHeadLocation(endpoint string) (string, string) {
	ctx := s.Config.Context
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return "", "invalid_source"
	}
	req.Header.Set("User-Agent", "KeepUpdated/0.2")
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", networkCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect {
		return "", "http_error"
	}
	location := resp.Header.Get("Location")
	if !publicURL(location, false) {
		return "", "identity_mismatch"
	}
	return location, ""
}
