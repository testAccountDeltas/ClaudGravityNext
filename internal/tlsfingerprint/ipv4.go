// Part of the tlsfingerprint package — forces direct connections to Google
// over IPv4.
package tlsfingerprint

import (
	"context"
	"net"
	"time"
)

// directNetwork is the network used for direct (non-proxied) connections to
// Google. Cloud Code rejects requests arriving over a host's IPv6 address
// whose geo-location Google deems unsupported ("User location is not
// supported for the API use", HTTP 400) even when the same host's IPv4 is
// accepted, and Go prefers IPv6 when the host has a global v6 address. Pinning
// to tcp4 makes the gateway independent of the server's IPv6 setup.
const directNetwork = "tcp4"

// dialIPv4 dials with the IPv4-only network, keeping the standard timeouts.
func dialIPv4(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: fingerprintDialTimeout, KeepAlive: 30 * time.Second}
	if network == "tcp" {
		network = directNetwork
	}
	return d.DialContext(ctx, network, addr)
}
