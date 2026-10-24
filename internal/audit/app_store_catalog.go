package audit

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var storeCountry = regexp.MustCompile(`^[A-Za-z]{2}$`)

func (s *Scanner) appStoreVersion(i *Item) (string, string) {
	i.AppStore = nil
	i.Latest = ""
	var m struct {
		Results []struct {
			BundleID         string          `json:"bundleId"`
			Kind             string          `json:"kind"`
			Version          string          `json:"version"`
			TrackID          json.RawMessage `json:"trackId"`
			SupportedDevices []string        `json:"supportedDevices"`
		} `json:"results"`
	}
	country := strings.ToLower(s.Config.Country)
	if !storeCountry.MatchString(country) {
		return "", "invalid_store_country"
	}
	code := s.get("https://itunes.apple.com/lookup?bundleId="+url.QueryEscape(i.Identity)+"&entity=macSoftware&country="+country, &m)
	if code != "" {
		return "", code
	}
	match := -1
	for n, r := range m.Results {
		mac := false
		for _, device := range r.SupportedDevices {
			mac = mac || device == "MacDesktop-MacDesktop"
		}
		if r.BundleID != i.Identity || !(r.Kind == "mac-software" || r.Kind == "software" && mac) {
			continue
		}
		if match >= 0 {
			return "", "ambiguous_store_results"
		}
		match = n
	}
	if match < 0 {
		return "", "no_matching_mac_product"
	}
	r := m.Results[match]
	var id int64
	_ = json.Unmarshal(r.TrackID, &id)
	version := r.Version
	if r.Kind == "software" {
		if id <= 0 {
			return "", "store_product_id_unverified"
		}
		version, code = s.macStoreVersion(id, i.Identity, country)
		if code != "" {
			return "", code
		}
	}
	if version == "" {
		return "", "no_matching_mac_product"
	}
	if id > 0 {
		i.AppStore = &AppStoreProduct{ID: id, BundleID: i.Identity, Country: country}
	}
	return version, ""
}

// Universal products can expose an iOS version in the ordinary lookup response.
func (s *Scanner) macStoreVersion(id int64, bundle, country string) (string, string) {
	key := strconv.FormatInt(id, 10)
	var m struct {
		Results map[string]struct {
			ID             string   `json:"id"`
			BundleID       string   `json:"bundleId"`
			Kind           string   `json:"kind"`
			DeviceFamilies []string `json:"deviceFamilies"`
			Offers         []struct {
				Assets []struct {
					Flavor string `json:"flavor"`
				} `json:"assets"`
				Version struct {
					Display string `json:"display"`
				} `json:"version"`
			} `json:"offers"`
		} `json:"results"`
		Meta struct {
			Storefront struct {
				Country string `json:"cc"`
			} `json:"storefront"`
		} `json:"meta"`
	}
	code := s.get("https://uclient-api.itunes.apple.com/WebObjects/MZStorePlatform.woa/wa/lookup?version=2&id="+key+"&p=mdm-lockup&caller=MDM&platform=macappstore&cc="+country, &m)
	if code != "" {
		return "", code
	}
	r, ok := m.Results[key]
	mac := false
	for _, family := range r.DeviceFamilies {
		mac = mac || family == "mac"
	}
	if !ok || r.ID != key || r.BundleID != bundle || !mac || (r.Kind != "iosSoftware" && r.Kind != "macSoftware") || !strings.EqualFold(m.Meta.Storefront.Country, country) {
		return "", "mac_store_product_unverified"
	}
	version := ""
	for _, offer := range r.Offers {
		macAsset := false
		for _, asset := range offer.Assets {
			macAsset = macAsset || asset.Flavor == "macSoftware"
		}
		if !macAsset {
			continue
		}
		if offer.Version.Display == "" {
			return "", "mac_store_version_unverified"
		}
		if version != "" && version != offer.Version.Display {
			return "", "ambiguous_mac_store_versions"
		}
		version = offer.Version.Display
	}
	if version == "" {
		return "", "mac_store_version_unverified"
	}
	return version, ""
}
