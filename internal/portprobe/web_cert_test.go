package portprobe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func leafTemplate(cn string) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		// IsCA deliberately left false: typical printer/embedded leaf cert.
	}
}

// Self-signed leaf without BasicConstraints CA:TRUE must report SelfSigned.
func TestCertInfoSelfSignedNonCA(t *testing.T) {
	key := newKey(t)
	tmpl := leafTemplate("printer.local")
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if c.IsCA {
		t.Fatal("fixture must be non-CA")
	}
	ti := certInfo(c, time.Now())
	if !ti.SelfSigned {
		t.Fatalf("non-CA self-signed cert reported SelfSigned=false: %+v", ti)
	}
	if ti.Expired {
		t.Fatalf("unexpected expired: %+v", ti)
	}
}

// Leaf whose issuer DN differs from its subject (CA-signed) must not be self-signed.
func TestCertInfoCASignedNotSelfSigned(t *testing.T) {
	caKey := newKey(t)
	caTmpl := leafTemplate("Test CA")
	caTmpl.IsCA = true
	caTmpl.BasicConstraintsValid = true
	caTmpl.KeyUsage = x509.KeyUsageCertSign
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey := newKey(t)
	leafTmpl := leafTemplate("device.local")
	der, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if ti := certInfo(c, time.Now()); ti.SelfSigned {
		t.Fatalf("CA-signed cert reported SelfSigned=true: %+v", ti)
	}
}

// End-to-end: a TLS server presenting a non-CA self-signed leaf is detected by ProbeWeb.
func TestProbeWebTLSSelfSignedNonCA(t *testing.T) {
	key := newKey(t)
	tmpl := leafTemplate("192.0.2.1")
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<title>Printer</title>"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "https://")
	ip, port := splitAddr(t, hostport)
	ws := ProbeWeb(context.Background(), ip, port, "https")
	if ws.Error != "" || ws.TLS == nil || !ws.TLS.SelfSigned {
		t.Fatalf("%+v tls=%+v", ws, ws.TLS)
	}
}
