package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type applicationTransport func(*http.Request) (*http.Response, error)

func (f applicationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func archiveFixture(t *testing.T) (ApplicationArchive, []byte, *http.Client, string) {
	t.Helper()
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	b := []byte("signed complete archive")
	a := ApplicationArchive{FeedURL: "https://vendor.example/appcast.xml", URL: "https://vendor.example/App.zip", PublicKey: base64.StdEncoding.EncodeToString(pub), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b)), Length: int64(len(b)), Version: "2.0", Build: "20"}
	feed := fmt.Sprintf(`<rss xmlns:sparkle="%s"><channel><item><sparkle:version>20</sparkle:version><sparkle:shortVersionString>2.0</sparkle:shortVersionString><sparkle:minimumSystemVersion>13</sparkle:minimumSystemVersion><enclosure url="%s" length="%d" sparkle:edSignature="%s" /></item></channel></rss>`, sparkleNS, a.URL, a.Length, a.Signature)
	client := &http.Client{Transport: applicationTransport(func(r *http.Request) (*http.Response, error) {
		body := string(b)
		if r.URL.String() == a.FeedURL {
			body = feed
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Header: make(http.Header)}, nil
	})}
	return a, b, client, feed
}

func TestApplicationArchiveSignedSelectionAndDownload(t *testing.T) {
	a, b, client, _ := archiveFixture(t)
	actual, e := ResolveApplicationArchive(context.Background(), Item{Build: "10", Version: "1.0"}, a.FeedURL, a.PublicKey, "14.0", "arm64", client)
	if e != nil || actual != a {
		t.Fatalf("candidate %#v %v", actual, e)
	}
	got, e := DownloadApplicationArchive(context.Background(), actual, client)
	if e != nil || string(got) != string(b) {
		t.Fatalf("download %q %v", got, e)
	}
}

func TestApplicationArchiveRejectsUnsupportedAndAmbiguousFeeds(t *testing.T) {
	a, _, _, feed := archiveFixture(t)
	for name, body := range map[string]string{
		"delta":          strings.Replace(feed, "<enclosure ", `<enclosure sparkle:deltaFrom="10" `, 1),
		"pkg":            strings.Replace(feed, "App.zip", "App.pkg", 1),
		"unsigned":       strings.Replace(feed, a.Signature, "", 1),
		"http":           strings.Replace(feed, a.URL, "http://vendor.example/App.zip", 1),
		"private":        strings.Replace(feed, a.URL, "https://127.0.0.1/App.zip", 1),
		"credentials":    strings.Replace(feed, a.URL, "https://token@vendor.example/App.zip", 1),
		"query":          strings.Replace(feed, a.URL, "https://vendor.example/App.zip?token=secret", 1),
		"rollout":        strings.Replace(feed, "<item>", "<item><sparkle:phasedRolloutInterval>1000</sparkle:phasedRolloutInterval>", 1),
		"upgrade-policy": strings.Replace(feed, "<item>", "<item><sparkle:minimumAutoupdateVersion>20</sparkle:minimumAutoupdateVersion>", 1),
		"informational":  strings.Replace(feed, "<item>", "<item><sparkle:informationalUpdate/>", 1),
		"conflict":       strings.Replace(feed, "<item>", "<item><sparkle:version>99</sparkle:version>", 1),
		"too-new":        strings.Replace(feed, ">13<", ">99<", 1),
		"too-large":      strings.Replace(feed, fmt.Sprintf(`length="%d"`, a.Length), `length="9999999999"`, 1),
		"ambiguous":      strings.Replace(feed, "</item>", fmt.Sprintf(`<enclosure url="https://vendor.example/Other.zip" length="%d" sparkle:edSignature="%s"/></item>`, a.Length, a.Signature), 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: applicationTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			if got, e := ResolveApplicationArchive(context.Background(), Item{Build: "10"}, a.FeedURL, a.PublicKey, "14.0", "arm64", client); e == nil {
				t.Fatalf("accepted %#v", got)
			}
		})
	}
}

func TestApplicationArchiveRejectsCorruptLengthAndSignature(t *testing.T) {
	a, b, _, _ := archiveFixture(t)
	for _, body := range []string{string(b) + "x", string(b[:len(b)-1]), strings.Repeat("x", len(b))} {
		client := &http.Client{Transport: applicationTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}, nil
		})}
		if _, e := DownloadApplicationArchive(context.Background(), a, client); e == nil {
			t.Fatal("accepted corrupt bytes")
		}
	}
}

func TestApplicationArchiveRejectsUnsafeRedirect(t *testing.T) {
	a, _, _, _ := archiveFixture(t)
	client := &http.Client{Transport: applicationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != a.URL {
			t.Fatal("followed unsafe redirect")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1/secret"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, e := DownloadApplicationArchive(context.Background(), a, client); e == nil {
		t.Fatal("unsafe redirect accepted")
	}
}

func TestApplicationArchiveDMGFormatSelectionAndCompatibility(t *testing.T) {
	a, _, _, feed := archiveFixture(t)
	for _, format := range []string{"", "dmg"} {
		body := feed
		if format == "dmg" {
			body = strings.ReplaceAll(body, "App.zip", "App.dmg")
		}
		client := &http.Client{Transport: applicationTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		got, e := ResolveApplicationArchive(context.Background(), Item{Build: "10"}, a.FeedURL, a.PublicKey, "14", "arm64", client)
		if e != nil || got.Format != format {
			t.Fatal(got, e)
		}
		b, e := json.Marshal(got)
		if e != nil {
			t.Fatal(e)
		}
		if format == "" && strings.Contains(string(b), "format") {
			t.Fatal("legacy ZIP JSON changed", string(b))
		}
		if format == "dmg" && !strings.Contains(string(b), `"format":"dmg"`) {
			t.Fatal(string(b))
		}
	}
	for _, format := range []string{"", "zip", "DMG", "unknown"} {
		a.Format = format
		a.URL = "https://vendor.example/App.dmg"
		client := &http.Client{Transport: applicationTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid format reached network")
			return nil, nil
		})}
		if _, e := DownloadApplicationArchive(context.Background(), a, client); e == nil || e.Error() != "application_archive_format_changed" {
			t.Fatal(format, e)
		}
	}
	a.Format = "dmg"
	a.URL = "https://vendor.example/App.zip"
	if _, e := DownloadApplicationArchive(context.Background(), a, nil); e == nil || e.Error() != "application_archive_format_changed" {
		t.Fatal(e)
	}
}

type cancellingArchiveBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b cancellingArchiveBody) Read(p []byte) (int, error) {
	n, e := b.Reader.Read(p)
	b.cancel()
	return n, e
}
func (b cancellingArchiveBody) Close() error { return nil }

func TestApplicationArchiveCancellationIsNotSignatureFailure(t *testing.T) {
	for _, phase := range []string{"request", "body"} {
		t.Run(phase, func(t *testing.T) {
			a, b, _, _ := archiveFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &http.Client{Transport: applicationTransport(func(*http.Request) (*http.Response, error) {
				if phase == "request" {
					cancel()
					return nil, ctx.Err()
				}
				return &http.Response{StatusCode: 200, ContentLength: int64(len(b)), Body: cancellingArchiveBody{Reader: strings.NewReader(string(b)), cancel: cancel}}, nil
			})}
			if _, e := DownloadApplicationArchive(ctx, a, client); !errors.Is(e, context.Canceled) {
				t.Fatalf("%s: cancellation reported as %v", phase, e)
			}
		})
	}
}
