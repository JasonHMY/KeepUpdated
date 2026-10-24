package audit

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var numeric = regexp.MustCompile(`^v?([0-9]+(?:\.[0-9]+)*)(?:_([0-9]+))?$`)

// Compare only numeric releases, including Homebrew revisions. Other schemes need an adapter.
func compare(a, b string) (int, bool) {
	am, bm := numeric.FindStringSubmatch(a), numeric.FindStringSubmatch(b)
	if am == nil || bm == nil {
		return 0, false
	}
	ap, bp := strings.Split(am[1], "."), strings.Split(bm[1], ".")
	n := len(ap)
	if len(bp) > n {
		n = len(bp)
	}
	for j := 0; j < n; j++ {
		av, bv := uint64(0), uint64(0)
		var e error
		if j < len(ap) {
			av, e = strconv.ParseUint(ap[j], 10, 64)
			if e != nil {
				return 0, false
			}
		}
		if j < len(bp) {
			bv, e = strconv.ParseUint(bp[j], 10, 64)
			if e != nil {
				return 0, false
			}
		}
		if av < bv {
			return -1, true
		}
		if av > bv {
			return 1, true
		}
	}
	ar, br := uint64(0), uint64(0)
	var e error
	if am[2] != "" {
		ar, e = strconv.ParseUint(am[2], 10, 64)
		if e != nil {
			return 0, false
		}
	}
	if bm[2] != "" {
		br, e = strconv.ParseUint(bm[2], 10, 64)
		if e != nil {
			return 0, false
		}
	}
	if ar < br {
		return -1, true
	}
	if ar > br {
		return 1, true
	}
	return 0, true
}
func (s *Scanner) check() {
	total := 0
	for _, i := range s.Report.Items {
		if i.lookup != "" {
			total++
		}
	}
	progress := s.progress("updates", total)
	defer progress.finish()
	jobs := make(chan *Item)
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if s.cancelled() {
					continue
				}
				progress.begin(i.Name)
				s.checkItem(i)
				progress.done(i.Update.Status != "current" && i.Update.Status != "available")
			}
		}()
	}
	for n := range s.Report.Items {
		if s.cancelled() {
			break
		}
		if s.Report.Items[n].lookup != "" {
			jobs <- &s.Report.Items[n]
		}
	}
	close(jobs)
	wg.Wait()
}
func (s *Scanner) checkItem(i *Item) {
	if i.lookup == "" {
		return
	}
	if !s.Config.Online {
		i.Update = result("unknown", "online_query_required")
		return
	}
	if i.Source == "sparkle" {
		s.checkSparkle(i)
		return
	}
	if i.Source == "electron-generic" {
		s.checkElectron(i)
		return
	}
	if strings.HasPrefix(i.Source, "vendor:") {
		s.checkVendor(i)
		return
	}
	code := ""
	latest := ""
	candidate := false
	switch i.Source {
	case "homebrew/core":
		var m struct {
			Name     string `json:"name"`
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
			Revision int `json:"revision"`
		}
		code = s.get("https://formulae.brew.sh/api/formula/"+url.PathEscape(i.lookup)+".json", &m)
		if code == "" {
			if m.Name != i.lookup {
				code = "identity_mismatch"
			} else {
				latest = m.Versions.Stable
				if m.Revision > 0 {
					latest += fmt.Sprintf("_%d", m.Revision)
				}
			}
		}
	case "homebrew/cask":
		var m struct {
			Token      string         `json:"token"`
			Version    string         `json:"version"`
			Variations map[string]any `json:"variations"`
		}
		code = s.get("https://formulae.brew.sh/api/cask/"+url.PathEscape(i.lookup)+".json", &m)
		if code == "" {
			if m.Token != i.lookup {
				code = "identity_mismatch"
			} else if len(m.Variations) > 0 {
				code = "platform_variation_unresolved"
			} else {
				latest = m.Version
			}
		}
	case "npm-public-candidate", "npm-public":
		var m struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		code = s.get("https://registry.npmjs.org/"+url.PathEscape(i.lookup)+"/latest", &m)
		candidate = i.Source == "npm-public-candidate"
		if code == "" {
			if m.Name != i.Identity {
				code = "identity_mismatch"
			} else {
				latest = m.Version
			}
		}
	case "pypi-public-candidate":
		var m struct {
			Info struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"info"`
		}
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(i.lookup) {
			i.Update = result("unknown", "invalid_package_identity")
			return
		}
		code = s.pypiInfo(i.lookup, &m.Info)
		candidate = true
		if code == "" {
			if normalizePy(m.Info.Name) != normalizePy(i.Identity) {
				code = "identity_mismatch"
			} else {
				latest = m.Info.Version
			}
		}
	case "app-store":
		latest, code = s.appStoreVersion(i)
	default:
		return
	}
	if code == "not_found" && (i.Source == "homebrew/core" || i.Source == "homebrew/cask") {
		s.missingBrew(i)
		return
	}
	if code != "" {
		status := "error"
		switch code {
		case "platform_variation_unresolved", "identity_mismatch", "no_matching_mac_product", "ambiguous_store_results",
			"store_product_id_unverified", "mac_store_product_unverified", "mac_store_version_unverified", "ambiguous_mac_store_versions", "invalid_store_country":
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
	if candidate {
		i.Update = result("unknown", "public_registry_candidate_unverified_origin")
		return
	}
	installed := i.Version
	if i.Kind == "formula" {
		installed = i.ActiveVersion
	}
	setCompared(i, installed, latest)
}
func normalizePy(s string) string {
	return regexp.MustCompile(`[-_.]+`).ReplaceAllString(strings.ToLower(s), "-")
}
