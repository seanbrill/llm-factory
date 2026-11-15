package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// Guard is the network policy of fetch_url: it resolves a host, refuses
// loopback, private, link-local, metadata and other non-public addresses, and
// connects only to an address it checked. Every connection goes through it,
// so redirects and DNS answers that change between lookups (rebinding) are
// covered too: the address that is dialed is the address that was checked.
type Guard struct {
	// Resolve looks a host name up. nil uses the system resolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial connects to a checked "ip:port". nil uses a net.Dialer that checks
	// the address once more right before connecting.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// BlockedError reports a refused destination.
type BlockedError struct {
	Host   string
	Addr   string // the refused address ("" when the name itself is refused)
	Reason string
}

func (e *BlockedError) Error() string {
	if e.Addr != "" && e.Addr != e.Host {
		return fmt.Sprintf("refused to connect to %s (%s): %s", e.Host, e.Addr, e.Reason)
	}
	return fmt.Sprintf("refused to connect to %s: %s", e.Host, e.Reason)
}

// blockedNames are refused before any lookup. Their addresses would be refused
// anyway; refusing the names keeps the error clear and doesn't depend on DNS.
var blockedNames = []string{"localhost", "metadata", "instance-data", "ip6-localhost", "ip6-loopback"}

// blockedSuffixes cover loopback names, cloud metadata names
// (metadata.google.internal), the container hosts' names for the host
// (host.docker.internal, host.containers.internal) and local-network names.
var blockedSuffixes = []string{".localhost", ".internal", ".local", ".home.arpa", ".localdomain", ".lan", ".intranet", ".corp"}

// CheckHost refuses host names that only make sense on a private network:
// loopback and metadata names, local suffixes, and single-label names (which
// resolve through local search domains).
func CheckHost(host string) error {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" {
		return &BlockedError{Host: host, Reason: "no host"}
	}
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return nil // literal addresses are checked by address
	}
	for _, n := range blockedNames {
		if h == n {
			return &BlockedError{Host: host, Reason: "a local or metadata host name"}
		}
	}
	for _, s := range blockedSuffixes {
		if strings.HasSuffix(h, s) {
			return &BlockedError{Host: host, Reason: "a local-network host name"}
		}
	}
	if !strings.Contains(h, ".") {
		return &BlockedError{Host: host, Reason: "a single-label (local) host name"}
	}
	return nil
}

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT (also Alibaba's metadata 100.100.100.200)
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.88.99.0/24",  // 6to4 relay anycast
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, and 255.255.255.255
		"::/96",           // IPv4-compatible (deprecated)
		"64:ff9b:1::/48",  // local-use NAT64
		"100::/64",        // discard
		"2001::/32",       // Teredo
		"2001:db8::/32",   // documentation
		"fec0::/10",       // site-local (deprecated)
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// AddrBlocked reports whether fetch_url must not connect to ip, and why.
func AddrBlocked(ip netip.Addr) (bool, string) {
	if !ip.IsValid() {
		return true, "not an IP address"
	}
	ip = ip.WithZone("").Unmap()
	switch {
	case ip.IsLoopback():
		return true, "a loopback address"
	case ip.IsPrivate():
		return true, "a private-network address"
	case ip.IsLinkLocalUnicast():
		return true, "a link-local address (this includes cloud metadata services)"
	case ip.IsUnspecified():
		return true, "an unspecified address"
	case ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast():
		return true, "a multicast address"
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true, "a reserved or non-public address"
		}
	}
	// IPv6 forms that carry an IPv4 address: judge the IPv4 address.
	if ip.Is6() {
		b := ip.As16()
		if nat64.Contains(ip) {
			return AddrBlocked(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if sixToFour.Contains(ip) {
			return AddrBlocked(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	return false, ""
}

func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if g.Resolve != nil {
		return g.Resolve(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (g *Guard) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if g.Dial != nil {
		return g.Dial(ctx, network, addr)
	}
	d := &net.Dialer{
		Timeout: 15 * time.Second,
		// Belt and braces: check the address the socket really connects to.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return &BlockedError{Host: address, Addr: address, Reason: "unparseable address"}
			}
			if bad, why := AddrBlocked(ap.Addr()); bad {
				return &BlockedError{Host: address, Addr: ap.Addr().String(), Reason: why}
			}
			return nil
		},
	}
	return d.DialContext(ctx, network, addr)
}

// DialContext resolves address's host, drops every address AddrBlocked
// refuses, and connects to the first remaining one that answers. It is the
// DialContext of fetch_url's HTTP transport.
func (g *Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if err := CheckHost(host); err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = g.resolve(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("can't resolve %s: %w", host, err)
		}
	}
	var allowed []netip.Addr
	var firstBlock error
	for _, a := range addrs {
		if bad, why := AddrBlocked(a); bad {
			if firstBlock == nil {
				firstBlock = &BlockedError{Host: host, Addr: a.String(), Reason: why}
			}
			continue
		}
		allowed = append(allowed, a.WithZone("").Unmap())
	}
	if len(allowed) == 0 {
		if firstBlock != nil {
			return nil, firstBlock
		}
		return nil, fmt.Errorf("can't resolve %s: no addresses", host)
	}
	var lastErr error
	for _, a := range allowed {
		conn, err := g.dial(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		var be *BlockedError
		if errors.As(err, &be) || ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}
