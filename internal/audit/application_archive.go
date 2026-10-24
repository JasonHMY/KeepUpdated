package audit

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ApplicationArchiveLimit int64 = 512 << 20

// ApplicationArchive binds a full, authenticated archive to the existing appcast
// selector. It is a candidate, not proof that the application can be installed.
type ApplicationArchive struct {
	Format    string `json:"format,omitempty"`
	FeedURL   string `json:"feed_url"`
	URL       string `json:"url"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
	Length    int64  `json:"length"`
	Version   string `json:"version"`
	Build     string `json:"build"`
}

// ResolveApplicationArchive only reads the public feed embedded in a verified
// installed bundle. Callers must authenticate that bundle before trusting it.
func ResolveApplicationArchive(ctx context.Context, i Item, feedURL, key, osVersion, arch string, client *http.Client) (ApplicationArchive, error) {
	out := ApplicationArchive{}
	publicKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || !publicURL(feedURL, true) {
		return out, errors.New("application_signed_source_required")
	}
	client = applicationClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return out, errors.New("application_signed_source_required")
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, errors.New("application_feed_unavailable")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
	var feed appcast
	if err != nil || resp.StatusCode != http.StatusOK || len(b) > metadataLimit || xml.Unmarshal(b, &feed) != nil {
		return out, errors.New("application_feed_invalid")
	}
	selectSparkle(&i, feed, osVersion, arch)
	if i.Update.Status != "available" {
		return out, errors.New("application_" + i.Update.Code)
	}
	found := false
	for _, entry := range feed.Items {
		compatible, known := castCompatible(entry, osVersion, arch)
		if !known || !compatible || entry.Channel != "" {
			continue
		}
		for _, enc := range entry.Enclosures {
			v, short := strings.TrimSpace(entry.Version), strings.TrimSpace(entry.Short)
			if v == "" {
				v = strings.TrimSpace(enc.Version)
			}
			if short == "" {
				short = strings.TrimSpace(enc.Short)
			}
			if short == "" {
				short = v
			}
			matches, known := hardwareMatches(enc.Arch, arch)
			if v != i.LatestBuild || short != i.Latest || enc.Delta != "" || enc.Channel != "" || enc.OS != "" && enc.OS != "macos" || !matches || !known {
				continue
			}
			if entry.MinimumAuto != "" || entry.Rollout != "" || entry.ManualOnly || enc.InstallationType != "" && enc.InstallationType != "application" {
				return out, errors.New("application_release_policy_requires_vendor")
			}
			u, e := url.Parse(enc.URL)
			sig, e2 := base64.StdEncoding.DecodeString(enc.Signature)
			if e != nil || !publicURL(enc.URL, true) || u.RawQuery != "" || (!strings.HasSuffix(strings.ToLower(u.Path), ".zip") && !strings.HasSuffix(strings.ToLower(u.Path), ".dmg")) || e2 != nil || len(sig) != ed25519.SignatureSize || enc.Length <= 0 || enc.Length > ApplicationArchiveLimit {
				continue
			}
			next := ApplicationArchive{FeedURL: feedURL, URL: enc.URL, PublicKey: key, Signature: enc.Signature, Length: enc.Length, Version: short, Build: v}
			if strings.HasSuffix(strings.ToLower(u.Path), ".dmg") {
				next.Format = "dmg"
			}
			if found && next != out {
				return ApplicationArchive{}, errors.New("application_archive_ambiguous")
			}
			out, found = next, true
		}
	}
	if !found {
		return out, errors.New("application_signed_full_zip_required")
	}
	return out, nil
}

func applicationClient(client *http.Client) *http.Client {
	if client == nil {
		return publicClient(3 * time.Minute)
	}
	// Tests may supply a controlled transport; redirect restrictions still apply.
	copy := *client
	copy.CheckRedirect = publicClient(time.Minute).CheckRedirect
	copy.Jar = nil
	if copy.Timeout == 0 {
		copy.Timeout = 3 * time.Minute
	}
	return &copy
}

// DownloadApplicationArchive authenticates the complete bytes before a caller
// may parse or extract them. No cookies, credentials or installer execution.
func DownloadApplicationArchive(ctx context.Context, a ApplicationArchive, client *http.Client) ([]byte, error) {
	u, err := url.Parse(a.URL)
	ext := ".zip"
	if a.Format == "dmg" {
		ext = ".dmg"
	}
	if err != nil || a.Format != "" && a.Format != "dmg" || u.RawQuery != "" || !strings.HasSuffix(strings.ToLower(u.Path), ext) {
		return nil, errors.New("application_archive_format_changed")
	}
	key, e := base64.StdEncoding.DecodeString(a.PublicKey)
	sig, e2 := base64.StdEncoding.DecodeString(a.Signature)
	if e != nil || e2 != nil || len(key) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || a.Length <= 0 || a.Length > ApplicationArchiveLimit || !publicURL(a.URL, true) {
		return nil, errors.New("application_archive_binding_invalid")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if e != nil {
		return nil, errors.New("application_archive_binding_invalid")
	}
	resp, e := applicationClient(client).Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("application_download_failed")
	}
	defer resp.Body.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength >= 0 && resp.ContentLength != a.Length {
		return nil, errors.New("application_archive_length_changed")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, a.Length+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e != nil || int64(len(b)) != a.Length || !ed25519.Verify(key, b, sig) {
		return nil, errors.New("application_archive_signature_invalid")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return b, nil
}
