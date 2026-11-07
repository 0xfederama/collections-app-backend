package favicons

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	maxRedirects   = 3
	maxCandidates  = 3
	defaultDial    = 3 * time.Second
	defaultRequest = 5 * time.Second
	userAgent      = "collections-app-favicon/1"
)

// HTTPFetcher is the production Fetcher: it reads the origin's page for <link rel=icon>,
// then falls back to /favicon.ico. Every connection is checked at dial time.
type HTTPFetcher struct {
	client *http.Client
}

// Options tune the fetcher. The zero value is the production setting.
type Options struct {
	// AllowLoopback lets the client dial 127.0.0.0/8 and ::1. Tests only (httptest servers);
	// every other non-public range stays refused.
	AllowLoopback  bool
	DialTimeout    time.Duration
	RequestTimeout time.Duration
}

var _ Fetcher = (*HTTPFetcher)(nil)

// NewHTTPFetcher builds the SSRF-safe client.
func NewHTTPFetcher(o Options) *HTTPFetcher {
	if o.DialTimeout == 0 {
		o.DialTimeout = defaultDial
	}
	if o.RequestTimeout == 0 {
		o.RequestTimeout = defaultRequest
	}
	dialer := &net.Dialer{
		Timeout: o.DialTimeout,
		// Control runs on the resolved address of every connection attempt, redirects and
		// DNS rebinding included, so there is no window between check and use.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || blockedAddr(ap.Addr(), o.AllowLoopback) {
				return ErrBlocked
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                  nil, // a proxy would dial on our behalf, past the check
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    o.DialTimeout,
		ResponseHeaderTimeout:  o.RequestTimeout,
		MaxResponseHeaderBytes: 32 << 10,
		DisableKeepAlives:      true,
	}
	return &HTTPFetcher{client: &http.Client{
		Transport: tr,
		Timeout:   o.RequestTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			return nil
		},
	}}
}

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",     // "this network"
	"100.64.0.0/10", // CGNAT
	"192.0.0.0/24",  // IETF protocol assignments
	"198.18.0.0/15", // benchmarking
	"240.0.0.0/4",   // reserved, includes the broadcast address
	"fc00::/7",      // ULA (also covered by IsPrivate)
	"64:ff9b::/96",  // NAT64 embeds an IPv4 address
	"2002::/16",     // 6to4 embeds an IPv4 address
	"100::/64",      // discard-only
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// blockedAddr reports whether the server must not connect to ip. IPv4-mapped IPv6 is
// unmapped first, so ::ffff:10.0.0.1 is judged as 10.0.0.1.
func blockedAddr(ip netip.Addr, allowLoopback bool) bool {
	ip = ip.Unmap().WithZone("")
	if ip.IsLoopback() {
		return !allowLoopback
	}
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Fetch implements Fetcher.
func (f *HTTPFetcher) Fetch(ctx context.Context, origin string) (Icon, error) {
	base, err := url.Parse(origin)
	if err != nil {
		return Icon{}, ErrNoIcon
	}
	candidates, err := f.pageCandidates(ctx, base)
	if err != nil {
		return Icon{}, err
	}
	fallback := base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()
	if len(candidates) == 0 || candidates[len(candidates)-1] != fallback {
		candidates = append(candidates, fallback)
	}
	for _, c := range candidates {
		icon, err := f.fetchIcon(ctx, c)
		if err == nil {
			return icon, nil
		}
		if ctx.Err() != nil {
			return Icon{}, ctx.Err()
		}
	}
	return Icon{}, ErrNoIcon
}

// pageCandidates reads the origin's "/" and returns the icon URLs it declares. A blocked
// origin is an error; any other failure just means no candidates (the fallback still runs).
func (f *HTTPFetcher) pageCandidates(ctx context.Context, base *url.URL) ([]string, error) {
	root := base.ResolveReference(&url.URL{Path: "/"})
	resp, err := f.get(ctx, root.String())
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return nil, ErrBlocked
		}
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, MaxPageBytes))
	if err != nil && len(page) == 0 {
		return nil, nil
	}
	return iconLinks(string(page), resp.Request.URL), nil
}

func (f *HTTPFetcher) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	return resp, nil
}

func (f *HTTPFetcher) fetchIcon(ctx context.Context, rawURL string) (Icon, error) {
	resp, err := f.get(ctx, rawURL)
	if err != nil {
		return Icon{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Icon{}, ErrNoIcon
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxIconBytes+1))
	if err != nil {
		return Icon{}, fmt.Errorf("read icon: %w", err)
	}
	if len(data) == 0 || len(data) > MaxIconBytes {
		return Icon{}, ErrNoIcon
	}
	ct, ok := imageType(data)
	if !ok {
		return Icon{}, ErrNoIcon
	}
	return Icon{ContentType: ct, Data: data}, nil
}

// imageType sniffs the bytes (the declared Content-Type is not trusted). SVG is refused on
// purpose: it can carry script, and it sniffs as text anyway.
func imageType(data []byte) (string, bool) {
	ct := http.DetectContentType(data)
	switch ct {
	case "image/x-icon", "image/png", "image/gif", "image/jpeg", "image/webp":
		return ct, true
	}
	return "", false
}

var (
	linkTag  = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrPair = regexp.MustCompile(`(?is)([a-z][a-z0-9:_-]*)\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)`)
)

// iconLinks returns the absolute icon URLs of the page, in preference order: rel "icon" /
// "shortcut icon" first, then apple-touch-icon. Only http(s) targets are kept.
func iconLinks(page string, base *url.URL) []string {
	var icons, touch []string
	for _, tag := range linkTag.FindAllString(page, -1) {
		var rel, href string
		for _, m := range attrPair.FindAllStringSubmatch(tag, -1) {
			val := strings.Trim(m[2], `"'`)
			switch strings.ToLower(m[1]) {
			case "rel":
				rel = strings.ToLower(val)
			case "href":
				href = strings.TrimSpace(html.UnescapeString(val))
			}
		}
		if href == "" || rel == "" {
			continue
		}
		u, err := base.Parse(href)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		for _, tok := range strings.Fields(rel) {
			if tok == "icon" {
				icons = append(icons, u.String())
				break
			}
			if strings.HasPrefix(tok, "apple-touch-icon") {
				touch = append(touch, u.String())
				break
			}
		}
	}
	out := append(icons, touch...)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}
