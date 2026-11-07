package plugins

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// lucode.http: https to the allowlisted hosts and nowhere else. The host names are resolved here,
// the addresses are checked and the connection goes to the address that was checked, so a name
// cannot change its answer between the check and the dial. Every redirect goes through it again.

const (
	maxResponseBytes = 5 << 20
	maxRequestBytes  = 8 << 20
	defaultFetchTime = 30 * time.Second
	maxRedirects     = 5
)

// HTTPOptions are the seams tests use. Empty means the real network.
type HTTPOptions struct {
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	TLS     *tls.Config
}

// FetchRequest is one call to lucode.http.fetch.
type FetchRequest struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    string
	Timeout time.Duration
	Hosts   []string
}

type httpCaller struct {
	opts      HTTPOptions
	transport *http.Transport
}

func newHTTPCaller(o HTTPOptions) *httpCaller {
	if o.Resolve == nil {
		o.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if o.Dial == nil {
		var d net.Dialer
		o.Dial = d.DialContext
	}
	c := &httpCaller{opts: o}
	c.transport = &http.Transport{
		Proxy:                 nil, // no proxy from the environment: nothing ambient
		DialContext:           c.dial,
		TLSClientConfig:       o.TLS,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
	}
	return c
}

func (c *httpCaller) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = c.opts.Resolve(ctx, host); err != nil {
		return nil, fmt.Errorf("could not resolve %s", host)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("could not resolve %s", host)
	}
	var last error
	for _, ip := range ips {
		if !PublicIP(ip) {
			last = fmt.Errorf("%s resolves to %s, an address plugins cannot reach", host, ip)
			continue
		}
		conn, err := c.opts.Dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b::/96", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
		"2002::/16", // 6to4 can wrap a private v4 address
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// PublicIP reports whether an address is on the public internet: not loopback, private,
// link-local (which holds the cloud metadata services) or reserved.
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// HostAllowed matches a host name against the plugin's list. *.example.org matches names under
// it, and never example.org itself.
func HostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range allowed {
		if suffix, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == a {
			return true
		}
	}
	return false
}

func checkFetchURL(u *url.URL, hosts []string) error {
	if u.Scheme != "https" {
		return fmt.Errorf("lucode.http only speaks https, not %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("lucode.http does not take a user in the address")
	}
	if !HostAllowed(u.Hostname(), hosts) {
		return fmt.Errorf("the host %q is not in this plugin's http permission", u.Hostname())
	}
	return nil
}

var droppedHeaders = map[string]bool{
	"host": true, "connection": true, "transfer-encoding": true, "content-length": true,
	"cookie": true, "proxy-authorization": true, "proxy-connection": true, "te": true, "upgrade": true,
}

func (c *httpCaller) fetch(ctx context.Context, r FetchRequest) (map[string]any, error) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, errors.New("not a valid address")
	}
	if err := checkFetchURL(u, r.Hosts); err != nil {
		return nil, err
	}
	if len(r.Body) > maxRequestBytes {
		return nil, fmt.Errorf("the request body is larger than %d MB", maxRequestBytes>>20)
	}
	timeout := r.Timeout
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = defaultFetchTime
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := strings.ToUpper(r.Method)
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(r.Body))
	if err != nil {
		return nil, errors.New("this request cannot be made")
	}
	for k, v := range r.Headers {
		if !droppedHeaders[strings.ToLower(k)] {
			req.Header.Set(k, v)
		}
	}
	client := &http.Client{
		Transport: c.transport,
		// no Jar: no cookies
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return checkFetchURL(next.URL, r.Hosts)
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("the request failed: %w", ue.Err)
		}
		return nil, errors.New("the request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("the response could not be read: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("the response is larger than %d MB", maxResponseBytes>>20)
	}
	headers := map[string]any{}
	for k, v := range resp.Header {
		headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		parsed = nil
	}
	return map[string]any{"status": float64(resp.StatusCode), "headers": headers, "text": string(body), "json": parsed}, nil
}
