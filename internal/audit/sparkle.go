package audit

import (
	"encoding/xml"
	"strings"
)

const sparkleNS = "http://www.andymatuschak.org/xml-namespaces/sparkle"

type appcast struct {
	XMLName xml.Name   `xml:"rss"`
	Items   []castItem `xml:"channel>item"`
}
type castItem struct {
	invalid     bool
	ManualOnly  bool
	Version     string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle version"`
	Short       string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle shortVersionString"`
	Minimum     string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle minimumSystemVersion"`
	Maximum     string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle maximumSystemVersion"`
	Hardware    string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle hardwareRequirements"`
	Channel     string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle channel"`
	Rollout     string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle phasedRolloutInterval"`
	MinimumAuto string          `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle minimumAutoupdateVersion"`
	Enclosures  []castEnclosure `xml:"enclosure"`
}
type castEnclosure struct {
	InstallationType string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle installationType,attr"`
	URL              string `xml:"url,attr"`
	Signature        string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle edSignature,attr"`
	Length           int64  `xml:"length,attr"`
	Version          string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle version,attr"`
	Short            string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle shortVersionString,attr"`
	OS               string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle os,attr"`
	Arch             string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle arch,attr"`
	Delta            string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle deltaFrom,attr"`
	Channel          string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle channel,attr"`
}

func (s *Scanner) checkSparkle(i *Item) {
	b, code := s.feedBytes(i.lookup)
	if code != "" {
		status := "error"
		if code == "feed_requires_unsafe_or_authenticated_request" {
			status = "unknown"
		}
		i.Update = result(status, code)
		return
	}
	var feed appcast
	if xml.Unmarshal(b, &feed) != nil || len(feed.Items) == 0 {
		i.Update = result("error", "invalid_appcast")
		return
	}
	selectSparkle(i, feed, s.osVersion, s.Report.Architecture)
}
func selectSparkle(i *Item, feed appcast, osVersion, arch string) {
	bestVersion, bestShort := "", ""
	uncertain := false
	var best castItem
	for _, entry := range feed.Items {
		if entry.invalid {
			i.Update = result("unknown", "appcast_conflicting_metadata")
			return
		}
		for _, enc := range entry.Enclosures {
			if entry.Version != "" && enc.Version != "" && strings.TrimSpace(entry.Version) != strings.TrimSpace(enc.Version) || entry.Short != "" && enc.Short != "" && strings.TrimSpace(entry.Short) != strings.TrimSpace(enc.Short) {
				i.Update = result("unknown", "appcast_conflicting_metadata")
				return
			}
		}
		if strings.TrimSpace(entry.Channel) != "" {
			continue
		}
		compatible, known := castCompatible(entry, osVersion, arch)
		if !known {
			uncertain = true
			continue
		}
		if !compatible {
			continue
		}
		enclosures := entry.Enclosures
		if len(enclosures) == 0 {
			enclosures = []castEnclosure{{}}
		}
		for _, enc := range enclosures {
			if enc.Delta != "" || strings.TrimSpace(enc.Channel) != "" || enc.OS != "" && enc.OS != "macos" {
				continue
			}
			if enc.Arch != "" {
				ok, known := hardwareMatches(enc.Arch, arch)
				if !known {
					uncertain = true
				}
				if !ok {
					continue
				}
			}
			v, short := strings.TrimSpace(entry.Version), strings.TrimSpace(entry.Short)
			if v == "" {
				v = strings.TrimSpace(enc.Version)
			}
			if short == "" {
				short = strings.TrimSpace(enc.Short)
			}
			if _, ok := compare(v, v); !ok {
				uncertain = true
				continue
			}
			if bestVersion == "" {
				bestVersion, bestShort, best = v, short, entry
				continue
			}
			cmp, _ := compare(v, bestVersion)
			if cmp > 0 {
				bestVersion, bestShort, best = v, short, entry
			}
		}
	}
	if bestVersion == "" {
		i.Update = result("unknown", "no_compatible_default_channel_release")
		return
	}
	i.Latest = bestShort
	if i.Latest == "" {
		i.Latest = bestVersion
	}
	i.LatestBuild = bestVersion
	i.Evidence = append(i.Evidence, "Sparkle static default-channel feed", "feed-constraints:macOS/architecture")
	if best.Rollout != "" {
		i.Evidence = append(i.Evidence, "phased-rollout:manual-availability-only")
	}
	if best.MinimumAuto != "" {
		i.Evidence = append(i.Evidence, "major-upgrade-policy:not-evaluated")
	}
	if uncertain {
		i.Update = result("unknown", "feed_constraints_unresolved")
		return
	}
	setCompared(i, i.Build, bestVersion)
}
func castCompatible(c castItem, osVersion, arch string) (bool, bool) {
	for _, b := range []struct {
		v       string
		minimum bool
	}{{strings.TrimSpace(c.Minimum), true}, {strings.TrimSpace(c.Maximum), false}} {
		if b.v == "" {
			continue
		}
		cmp, known := compare(osVersion, b.v)
		if !known {
			return false, false
		}
		if b.minimum && cmp < 0 || !b.minimum && cmp > 0 {
			return false, true
		}
	}
	return hardwareMatches(strings.TrimSpace(c.Hardware), arch)
}
func hardwareMatches(requirement, arch string) (bool, bool) {
	if requirement == "" {
		return true, true
	}
	if arch == "amd64" {
		arch = "x86_64"
	}
	matched := false
	for _, a := range strings.FieldsFunc(requirement, func(r rune) bool { return r == ',' || r == ' ' }) {
		if a != "arm64" && a != "x86_64" {
			return false, false
		}
		if a == arch {
			matched = true
		}
	}
	return matched, true
}
func setCompared(i *Item, installed, latest string) {
	cmp, known := compare(installed, latest)
	if !known {
		i.Update = result("unknown", "version_scheme_or_active_version_unresolved")
	} else if cmp < 0 {
		i.Update = result("available", "newer_release")
	} else if cmp == 0 {
		i.Update = result("current", "exact_source_version_match")
	} else {
		i.Update = result("unknown", "installed_newer_than_source")
	}
}

func (c *castItem) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	seen := map[xml.Name]bool{}
	fields := map[string]*string{"version": &c.Version, "shortVersionString": &c.Short, "minimumSystemVersion": &c.Minimum, "maximumSystemVersion": &c.Maximum, "hardwareRequirements": &c.Hardware, "channel": &c.Channel, "phasedRolloutInterval": &c.Rollout, "minimumAutoupdateVersion": &c.MinimumAuto}
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if dst, ok := fields[t.Name.Local]; ok && t.Name.Space == sparkleNS {
				if seen[t.Name] {
					c.invalid = true
				}
				seen[t.Name] = true
				if err := d.DecodeElement(dst, &t); err != nil {
					return err
				}
			} else if t.Name.Space == sparkleNS && (t.Name.Local == "informationalUpdate" || t.Name.Local == "criticalUpdate" || t.Name.Local == "installationType") {
				c.ManualOnly = true
				if err := d.Skip(); err != nil {
					return err
				}
			} else if t.Name.Local == "enclosure" && t.Name.Space == "" {
				attrs := map[xml.Name]bool{}
				for _, a := range t.Attr {
					if attrs[a.Name] {
						c.invalid = true
					}
					attrs[a.Name] = true
				}
				var enclosure castEnclosure
				if err := d.DecodeElement(&enclosure, &t); err != nil {
					return err
				}
				c.Enclosures = append(c.Enclosures, enclosure)
			} else {
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}
