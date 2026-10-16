package cli

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// tailnetRanges are the addresses Tailscale gives nodes.
var tailnetRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

// Transport returns the HTTP transport of the CLI. tend's server is on the
// tailnet, which a proxy outside it cannot reach, so requests to tailnet
// names and addresses go direct; others follow the proxy environment.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		if onTailnet(r.URL.Hostname()) {
			return nil, nil
		}
		return http.ProxyFromEnvironment(r)
	}
	return t
}

// onTailnet reports whether host names a tailnet node: a MagicDNS name, a
// short name only the tailnet resolves, or a tailnet address.
func onTailnet(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		for _, p := range tailnetRanges {
			if p.Contains(ip.Unmap()) {
				return true
			}
		}
		return false
	}
	return strings.HasSuffix(host, ".ts.net") || (host != "" && !strings.Contains(host, ".") && net.ParseIP(host) == nil)
}
