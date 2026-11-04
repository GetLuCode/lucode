package natsbus

import (
	"net"
	"strings"
)

// DefaultPort is NATS's own port, used when a URL does not name one.
const DefaultPort = "4222"

// NormalizeURL completes what is missing from a NATS server address, so that
// `--nats-url 192.168.1.5`, `--nats-url hub` and `--nats-url hub:4223` all work:
// the scheme defaults to nats:// and the port to 4222. A comma-separated list is
// normalized item by item. Complete URLs come back unchanged.
func NormalizeURL(s string) string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = normalizeOne(strings.TrimSpace(p))
	}
	return strings.Join(parts, ",")
}

func normalizeOne(u string) string {
	if u == "" {
		return u
	}
	scheme, rest := "nats://", u
	if i := strings.Index(u, "://"); i >= 0 {
		scheme, rest = u[:i+3], u[i+3:]
	}
	hostport, tail := rest, ""
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		hostport, tail = rest[:i], rest[i:]
	}
	userinfo := ""
	if i := strings.LastIndex(hostport, "@"); i >= 0 {
		userinfo, hostport = hostport[:i+1], hostport[i+1:]
	}
	if _, _, err := net.SplitHostPort(hostport); err != nil { // no port
		switch {
		case strings.HasPrefix(hostport, "["): // [::1]
		case strings.Count(hostport, ":") > 1: // ::1, a bare IPv6 address
			hostport = "[" + hostport + "]"
		}
		hostport += ":" + DefaultPort
	}
	return scheme + userinfo + hostport + tail
}
