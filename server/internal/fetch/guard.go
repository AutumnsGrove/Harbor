// The SSRF guard. Harbor fetches arbitrary URLs from inside the tailnet, so a
// saved link is a potential instruction to read the router, the potato's own
// admin port or another tailnet machine. Two checks enforce the same policy:
// the URL check refuses obvious shapes before connecting, and the dialer's
// Control hook checks the resolved address, which is the one that counts
// (plans/03 test 1).
package fetch

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
)

// Blocked ranges that net.IP's helpers do not cover. 100.64/10 is CGNAT and
// Tailscale's IPv4 range; fd7a:115c:a1e0::/48 is this tailnet's IPv6 prefix.
// All of fc00::/7 is refused, not just that one /48: a ULA is never a public
// page.
var blockedRanges = mustCIDRs(
	"100.64.0.0/10",
	"192.0.0.0/24",  // IETF protocol assignments
	"198.18.0.0/15", // benchmarking
	"fc00::/7",      // unique local addresses, including fd7a:115c:a1e0::/48
	"fd7a:115c:a1e0::/48",
	"64:ff9b::/96",  // NAT64: could wrap a private IPv4 address
	"2001:db8::/32", // documentation
)

func mustCIDRs(raw ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(raw))
	for _, cidr := range raw {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("fetch: bad blocked CIDR " + cidr + ": " + err.Error())
		}
		out = append(out, network)
	}
	return out
}

// validateURL is the pre-connection half of the policy: scheme, credentials,
// port and hostname. It runs for the first URL and for every redirect hop.
func (c *Client) validateURL(u *url.URL) error {
	if c.allowUnsafe {
		return nil
	}
	if u == nil {
		return fmt.Errorf("%w: empty URL", ErrUnsupportedURL)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrUnsupportedURL, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: the URL carries credentials", ErrBlocked)
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return fmt.Errorf("%w: port %s is not 80 or 443", ErrBlocked, port)
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	switch {
	case host == "":
		return fmt.Errorf("%w: no host", ErrUnsupportedURL)
	case host == "localhost", strings.HasSuffix(host, ".localhost"):
		return fmt.Errorf("%w: %s is loopback", ErrBlocked, host)
	case host == "local", strings.HasSuffix(host, ".local"):
		return fmt.Errorf("%w: %s is a local name", ErrBlocked, host)
	case host == "ts.net", strings.HasSuffix(host, ".ts.net"):
		return fmt.Errorf("%w: %s is a tailnet name", ErrBlocked, host)
	}

	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip)
	}
	return nil
}

// checkIP is the policy for one resolved address. It is exported to tests only
// through its callers; nothing outside this package chooses an address.
func checkIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("%w: address did not resolve to an IP", ErrBlocked)
	}
	// 0.0.0.0 is the answer a filtering resolver gives — AdGuard and its
	// relatives rewrite a blocked A record to it — and it is a different thing
	// from a private address. Saying so is the difference between a one-line
	// diagnosis and an hour spent reading this guard for a bug that is not here
	// (issue #77).
	if ip.IsUnspecified() {
		return fmt.Errorf("%w: the local resolver answered %s, which is a filtered domain rather than a public address", ErrBlocked, ip)
	}
	if ip.IsLoopback() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return fmt.Errorf("%w: %s is not a public address", ErrBlocked, ip)
	}
	for _, network := range blockedRanges {
		if network.Contains(ip) {
			return fmt.Errorf("%w: %s is in %s", ErrBlocked, ip, network)
		}
	}
	return nil
}

// dialControl runs on the address that is about to be connected to, after DNS.
// Checking here rather than on the hostname is what defeats a resolver that
// answers a public address first and a private one second.
func (c *Client) dialControl(_, address string, _ syscall.RawConn) error {
	if c.allowUnsafe {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	return checkIP(net.ParseIP(host))
}
