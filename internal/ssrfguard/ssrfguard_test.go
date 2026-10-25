package ssrfguard

import (
	"net"
	"strings"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	blocked := []string{
		// loopback, unspecified, private, link-local (incl. cloud metadata)
		"127.0.0.1", "127.255.255.254", "::1", "0.0.0.0", "::",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", "fc00::1", "fd12:3456::1",
		"169.254.169.254", "fe80::1", "ff02::1", "224.0.0.1",
		// shared / reserved IPv4
		"100.64.0.1", "100.127.255.254", "0.1.2.3", "240.0.0.1", "255.255.255.255",
		"192.0.0.1", "192.0.2.1", "198.18.0.1", "198.19.255.254", "198.51.100.7", "203.0.113.5", "192.88.99.1",
		// IPv4-mapped
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.1.2.3",
		// IPv6 forms that carry an IPv4 address
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::a00:1",
		"2002:7f00:1::", "2002:a9fe:a9fe::1", "2002:c0a8:101::",
		"::127.0.0.1", "::10.0.0.1", "::169.254.169.254",
		// reserved IPv6
		"2001::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "100::1", "fec0::1", "2001:db8::1", "64:ff9b:1::1",
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test address %q", s)
		}
		if !blockedIP(ip) {
			t.Errorf("%s must be blocked", s)
		}
	}
	allowed := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.15.255.255", "172.32.0.1", "100.63.255.255", "100.128.0.1",
		"198.17.255.255", "198.20.0.1", "2001:4860:4860::8888", "2606:4700:4700::1111",
		// translated PUBLIC addresses stay reachable
		"64:ff9b::808:808", "2002:808:808::1", "::8.8.8.8", "::ffff:8.8.8.8",
	}
	for _, s := range allowed {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("%s is public and must not be blocked", s)
		}
	}
	if !blockedIP(nil) {
		t.Error("a nil address must be blocked")
	}
}

func TestSafeResolve_LiteralAddresses(t *testing.T) {
	if _, err := SafeResolve("127.0.0.1"); err == nil || !strings.Contains(err.Error(), "blocked target address") {
		t.Fatalf("loopback literal: err = %v", err)
	}
	if _, err := SafeResolve("64:ff9b::7f00:1"); err == nil {
		t.Fatal("NAT64-wrapped loopback must be refused")
	}
	ips, err := SafeResolve("8.8.8.8")
	if err != nil || len(ips) != 1 || !ips[0].Equal(net.ParseIP("8.8.8.8")) {
		t.Fatalf("public literal: ips=%v err=%v", ips, err)
	}
}

func stubDNS(t *testing.T, ips []net.IP, err error) {
	t.Helper()
	orig := lookupIP
	lookupIP = func(string) ([]net.IP, error) { return ips, err }
	t.Cleanup(func() { lookupIP = orig })
}

func TestSafeResolve_NameResolvingToLoopbackIsRefused(t *testing.T) {
	stubDNS(t, []net.IP{net.ParseIP("127.0.0.1")}, nil)
	if _, err := SafeResolve("evil.example"); err == nil {
		t.Fatal("a name that resolves to loopback must be refused")
	}
}

func TestSafeResolve_OneBadAddressRejectsTheWholeAnswer(t *testing.T) {
	stubDNS(t, []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.1")}, nil)
	if _, err := SafeResolve("rebind.example"); err == nil {
		t.Fatal("an answer with any internal address must be refused as a whole")
	}
}

func TestSafeResolve_ResolverFailureAndEmptyAnswer(t *testing.T) {
	stubDNS(t, nil, net.UnknownNetworkError("boom"))
	if _, err := SafeResolve("x.example"); err == nil {
		t.Fatal("a resolver error must surface")
	}
	stubDNS(t, nil, nil)
	if _, err := SafeResolve("x.example"); err == nil {
		t.Fatal("an empty answer must be an error, not success")
	}
}

func TestSafeResolve_IPv4IsListedFirst(t *testing.T) {
	stubDNS(t, []net.IP{net.ParseIP("2001:4860:4860::8888"), net.ParseIP("8.8.4.4"), net.ParseIP("2606:4700:4700::1111"), net.ParseIP("8.8.8.8")}, nil)
	ips, err := SafeResolve("dual.example")
	if err != nil || len(ips) != 4 {
		t.Fatalf("ips=%v err=%v", ips, err)
	}
	if ips[0].To4() == nil || ips[1].To4() == nil || ips[2].To4() != nil {
		t.Fatalf("IPv4 addresses must come first, got %v", ips)
	}
	if !ips[0].Equal(net.ParseIP("8.8.4.4")) || !ips[1].Equal(net.ParseIP("8.8.8.8")) {
		t.Fatalf("relative order inside a family must be kept, got %v", ips)
	}
}
