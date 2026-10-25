// Package mitm is a port of superrelay (v1)'s mitm.go, split across six
// files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3: ca.go
// (this file — MitmCA + bufferedConn), auth.go (ProxyAuthOK), tunnel.go
// (isAnthropicHost/the SSRF subnet checks/HandleConnectTunnel), relay.go
// (idleCopy/serveInterceptedTLS), responsewriter.go (MitmResponseWriter),
// bootstrap.go (HandleBootstrapSpoof/PassthroughToReal).
//
// Per Part 3 p.5 and Part 7.2 of the spec, this package is EXPLICITLY
// frozen: every function body below is byte-for-byte unchanged from v1,
// verified against the characterization tests in
// superrelay/mitm_tlsfreeze_test.go (ported alongside this package, see
// mitm_characterization_test.go) BEFORE this port was written, not after.
// This is SSRF-validation and CONNECT-tunnel logic — "improve it while
// you're in there" is explicitly out of scope for this phase.
package mitm

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// bufferedConn prepends bytes net/http's bufio.Reader already pulled off the
// socket (see HandleConnectTunnel's Hijack() call) ahead of further reads
// from the raw connection, so nothing read during CONNECT header parsing is
// lost to the TLS handshake that follows.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}

// GlobalMitmCA is set once in cmd/gateway/main.go right after InitMitmCA
// succeeds, so other handlers (the /api/bootstrap installer endpoint) can
// hand out the CA's PEM without needing mitmCA threaded through as a
// parameter. Exported (unlike v1's package-private globalMitmCA) because
// this is now a separate package from its callers.
var GlobalMitmCA *MitmCA

type MitmCA struct {
	caCert    *x509.Certificate
	caKey     *rsa.PrivateKey
	caPEM     []byte
	caPEMPath string
	certCache sync.Map
	certMu    sync.Mutex
}

func InitMitmCA(caDir string) (*MitmCA, error) {
	if err := os.MkdirAll(caDir, 0755); err != nil {
		return nil, err
	}
	caCertPath := filepath.Join(caDir, "superrelay-ca.pem")
	caKeyPath := filepath.Join(caDir, "superrelay-ca.key")

	var caCert *x509.Certificate
	var caKey *rsa.PrivateKey
	var caPEM []byte

	certBytes, errCert := os.ReadFile(caCertPath)
	keyBytes, errKey := os.ReadFile(caKeyPath)

	if errCert == nil && errKey == nil {
		block, _ := pem.Decode(certBytes)
		if block != nil {
			caCert, _ = x509.ParseCertificate(block.Bytes)
		}
		keyBlock, _ := pem.Decode(keyBytes)
		if keyBlock != nil {
			caKey, _ = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		}
		caPEM = certBytes
	}

	if caCert == nil || caKey == nil {
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		template := x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject: pkix.Name{
				Organization: []string{"SuperRelay Internal CA"},
				CommonName:   "SuperRelay Local Root CA",
			},
			NotBefore:             time.Now().Add(-24 * time.Hour),
			NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			BasicConstraintsValid: true,
			IsCA:                  true,
			MaxPathLen:            2,
		}
		der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
		if err != nil {
			return nil, err
		}
		caCert, _ = x509.ParseCertificate(der)
		caKey = priv
		caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

		_ = os.WriteFile(caCertPath, caPEM, 0644)
		_ = os.WriteFile(caKeyPath, keyPEM, 0600)
	}

	return &MitmCA{
		caCert:    caCert,
		caKey:     caKey,
		caPEM:     caPEM,
		caPEMPath: caCertPath,
	}, nil
}

// GetCAPEM returns the CA certificate in PEM form, for handing to clients
// that need to trust it (e.g. the /api/bootstrap installer endpoint) rather
// than reading the file off whatever disk the gateway happens to run on.
func (m *MitmCA) GetCAPEM() string {
	return string(m.caPEM)
}

func (m *MitmCA) GetCAPath() string {
	abs, err := filepath.Abs(m.caPEMPath)
	if err == nil {
		return abs
	}
	return m.caPEMPath
}

func (m *MitmCA) GetCertificate(domain string) (tls.Certificate, error) {
	domain = strings.ToLower(domain)
	if val, ok := m.certCache.Load(domain); ok {
		return val.(tls.Certificate), nil
	}

	m.certMu.Lock()
	defer m.certMu.Unlock()
	if val, ok := m.certCache.Load(domain); ok {
		return val.(tls.Certificate), nil
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	// domain may be a hostname (goes in DNSNames) or a literal IP (goes in
	// IPAddresses) — TLS hostname verification checks the IP SAN list, not
	// DNSNames, when the client dials a bare IP (e.g. a remote server's
	// public address), so a string match alone would still fail there.
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if ip := net.ParseIP(domain); ip != nil {
		ips = append(ips, ip)
	} else {
		dnsNames = append(dnsNames, domain)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		serialNumber = big.NewInt(time.Now().UnixNano())
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   domain,
			Organization: []string{"SuperRelay Internal Proxy"},
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, m.caCert, &priv.PublicKey, m.caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	m.certCache.Store(domain, tlsCert)
	return tlsCert, nil
}
