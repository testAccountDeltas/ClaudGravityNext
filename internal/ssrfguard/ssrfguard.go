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
		resolved, err := net.LookupIP(host)
		if err != nil {
			return nil, err
		}
		ips = resolved
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses found for %s", host)
	}
	for _, ip := range ips {
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || (ip.To4() != nil && (cgnatSubnet.Contains(ip.To4()) || thisNetworkSubnet.Contains(ip.To4()) || classESubnet.Contains(ip.To4()))) {
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
