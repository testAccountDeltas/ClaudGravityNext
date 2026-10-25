// Package ssrfguard is the single owner of "is this target address safe to
// make an outbound request to" — the same resolve-then-check-every-IP logic
// internal/mitm/tunnel.go's checkConnectTarget needed for CONNECT targets,
// extracted here so internal/translate's own outbound fetch (GAP-T9: a
// url-sourced image/document attachment) can reuse it instead of growing a
// second, divergent copy. Both callers share one blocklist, so a future
// bypass fix (like the 0.0.0.0/8 and 240.0.0.0/4 additions below, found by
// prior audits) only needs to land once.
package ssrfguard

import (
	"fmt"
	"net"
	"sort"
)

// cgnatSubnet is the shared address space 100.64.0.0/10 (RFC 6598) — not
// covered by net.IP.IsPrivate(), which only recognizes RFC 1918.
var cgnatSubnet = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

// thisNetworkSubnet is the reserved 0.0.0.0/8 "this network" block —
// net.IP.IsLoopback() does not cover it, but some stacks still route it to
// localhost/this-host services.
var thisNetworkSubnet = &net.IPNet{
	IP:   net.IPv4(0, 0, 0, 0),
	Mask: net.CIDRMask(8, 32),
}

// classESubnet is the reserved 240.0.0.0/4 block (RFC 1112/RFC 6890),
// including the limited broadcast address 255.255.255.255.
var classESubnet = &net.IPNet{
	IP:   net.IPv4(240, 0, 0, 0),
	Mask: net.CIDRMask(4, 32),
}

// Extra IPv4 ranges that are never a legitimate public target: IETF protocol
// assignments, benchmarking, the 6to4 relay anycast block and the three
// documentation (TEST-NET) blocks.
var reservedV4 = mustCIDRs("192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24")

// reservedV6 are IPv6 ranges refused outright: Teredo (the embedded client
// address is obfuscated), the discard prefix, deprecated site-local, the
// documentation prefix and the local-use NAT64 prefix.
var reservedV6 = mustCIDRs("2001::/32", "100::/64", "fec0::/10", "2001:db8::/32", "64:ff9b:1::/48")

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, len(cidrs))
	for i, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out[i] = n
	}
	return out
}

func inAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// blockedIP reports whether ip must never be dialled. IPv6 forms that carry an
// IPv4 address (NAT64 64:ff9b::/96, 6to4 2002::/16, IPv4-compatible ::a.b.c.d)
// are judged by the IPv4 address inside them, so a translated loopback or
// metadata address is refused exactly like the plain one.
func blockedIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return cgnatSubnet.Contains(v4) || thisNetworkSubnet.Contains(v4) || classESubnet.Contains(v4) || inAny(v4, reservedV4)
	}
	v6 := ip.To16()
	if v6 == nil || inAny(v6, reservedV6) {
		return true
	}
	allZero := func(b []byte) bool {
		for _, x := range b {
			if x != 0 {
				return false
			}
		}
		return true
	}
	switch {
	case v6[0] == 0x00 && v6[1] == 0x64 && v6[2] == 0xff && v6[3] == 0x9b && allZero(v6[4:12]): // 64:ff9b::/96
		return blockedIP(net.IP(v6[12:16]))
	case v6[0] == 0x20 && v6[1] == 0x02: // 2002::/16, IPv4 in bytes 2..5
		return blockedIP(net.IP(v6[2:6]))
	case allZero(v6[:12]): // ::/96 IPv4-compatible
		return blockedIP(net.IP(v6[12:16]))
	}
	return false
}

// lookupIP is the DNS seam; tests replace it.
var lookupIP = net.LookupIP

// SafeResolve resolves host and reports every verified-safe IP it has (IPv4
// first — a dual-stack answer commonly lists AAAA before A, and a host with
// no outbound IPv6 route fails if only the first-returned address is ever
// tried), or an error if host is unresolvable or resolves to any private,
// loopback, link-local, multicast, CGNAT, or reserved address — i.e. any
// address an attacker could use to make this process reach an internal
// service or cloud metadata endpoint instead of the public internet.
func SafeResolve(host string) ([]net.IP, error) {
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		resolved, err := lookupIP(host)
		if err != nil {
			return nil, err
		}
		ips = resolved
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses found for %s", host)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return nil, fmt.Errorf("blocked target address: %s", ip)
		}
	}
	sorted := make([]net.IP, len(ips))
	copy(sorted, ips)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].To4() != nil && sorted[j].To4() == nil
	})
	return sorted, nil
}
