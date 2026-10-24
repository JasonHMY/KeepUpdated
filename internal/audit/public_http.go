package audit

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func publicURL(raw string, initial bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Fragment != "" || u.Port() != "" && u.Port() != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip, e := netip.ParseAddr(host); e == nil && !publicIP(ip) {
		return false
	}
	if initial {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return false
		}
		for k, vs := range query {
			if k != "channel" || len(vs) != 1 {
				return false
			}
			switch vs[0] {
			case "production", "stable", "release", "beta", "nightly", "dev":
			default:
				return false
			}
		}
	}
	return true
}
func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "fec0::/10", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2002::/16"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}
func publicClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, errors.New("no public address")
		}
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, errors.New("nonpublic address")
			}
		}
		d := net.Dialer{Timeout: timeout}
		var last error
		for _, ip := range ips {
			c, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return c, nil
			}
			last = e
		}
		return nil, last
	}}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || !publicURL(r.URL.String(), false) {
			return errors.New("unsafe redirect")
		}
		return nil
	}}
}
func (s *Scanner) feedBytes(raw string) ([]byte, string) {
	if !publicURL(raw, true) {
		return nil, "feed_requires_unsafe_or_authenticated_request"
	}
	req, e := http.NewRequest(http.MethodGet, raw, nil)
	if e != nil {
		return nil, "invalid_source"
	}
	if s.Config.Context != nil {
		req = req.WithContext(s.Config.Context)
	}
	req.Header.Set("User-Agent", "KeepUpdated/0.1")
	resp, e := s.feedClient.Do(req)
	if e != nil {
		return nil, networkCode(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, "not_found"
	}
	if resp.StatusCode != 200 {
		return nil, "http_error"
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
	if e != nil {
		return nil, networkCode(e)
	}
	if len(b) > metadataLimit {
		return nil, "output_limit"
	}
	return b, ""
}
