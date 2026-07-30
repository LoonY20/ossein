package ossein

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// selfSignedTLSConfig builds a throwaway certificate for 127.0.0.1, plus the
// pool a client needs to trust it.
func selfSignedTLSConfig(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(certificate)

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  key,
			Leaf:        certificate,
		}},
	}, pool
}

// TestServeListenerServesHTTPSThroughTLSListener shows how an application
// terminates HTTPS: wrap the listener with crypto/tls. No Ossein API is needed
// beyond ServeListener, and nothing about the protocol is inferred.
func TestServeListenerServesHTTPSThroughTLSListener(t *testing.T) {
	app := New(WithShutdownTimeout(time.Second))
	app.Get("/secure", func(c *Context) error {
		return c.JSON(http.StatusOK, map[string]string{"scheme": "https"})
	})

	tlsConfig, pool := selfSignedTLSConfig(t)
	listener := tls.NewListener(newLocalListener(t), tlsConfig)
	address := listener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- app.ServeListener(ctx, &http.Server{}, listener)
	}()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		Timeout:   5 * time.Second,
	}
	response, err := client.Get("https://" + address + "/secure")
	if err != nil {
		t.Fatalf("HTTPS request failed: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.StatusCode, body)
	}
	if response.TLS == nil {
		t.Fatal("expected the response to have been served over TLS")
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ServeListener() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeListener did not return after cancellation")
	}
}

// TestServeDoesNotInferTLSFromTLSConfig pins a deliberate choice: the protocol
// follows the method called, exactly as in net/http. Setting TLSConfig only to
// raise MinVersion must not silently turn a plain server into an HTTPS one.
func TestServeDoesNotInferTLSFromTLSConfig(t *testing.T) {
	app := New(WithShutdownTimeout(time.Second))
	app.Get("/plain", func(c *Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	listener := newLocalListener(t)
	server := &http.Server{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- app.ServeListener(ctx, server, listener)
	}()

	response, err := http.Get("http://" + listener.Addr().String() + "/plain")
	if err != nil {
		t.Fatalf("plain HTTP request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 over plain HTTP", response.StatusCode)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ServeListener() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeListener did not return")
	}
}

// TestServeTLSRequiresCertificates pins a comprehensible error. Left to the
// standard library, empty file paths surface as `open : no such file`, which
// says nothing about certificates.
func TestServeTLSRequiresCertificates(t *testing.T) {
	app := New()
	stopped := false
	app.OnStop(func(context.Context) error {
		stopped = true
		return nil
	})

	err := app.ServeTLS(context.Background(), &http.Server{Addr: "127.0.0.1:0"}, "", "")
	if err == nil {
		t.Fatal("expected an error when no certificate is available")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("error = %v, want it to mention certificates", err)
	}
	if !strings.Contains(err.Error(), "ossein") {
		t.Fatalf("error = %v, want it attributed to ossein", err)
	}
	if stopped {
		t.Fatal("stop hooks must not run when the request is rejected up front")
	}
}

// TestServeTLSUsesTLSConfigCertificates covers the Addr-based HTTPS path with
// certificates supplied through TLSConfig rather than files.
func TestServeTLSUsesTLSConfigCertificates(t *testing.T) {
	app := New(WithShutdownTimeout(time.Second))
	app.Get("/secure", func(c *Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	tlsConfig, pool := selfSignedTLSConfig(t)

	// Reserve an address, release it, and hand it to ServeTLS. Serve binds by
	// address here, so a brief window exists; the request loop tolerates it.
	probe := newLocalListener(t)
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- app.ServeTLS(ctx, &http.Server{Addr: address, TLSConfig: tlsConfig}, "", "")
	}()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		Timeout:   5 * time.Second,
	}

	var response *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get("https://" + address + "/secure")
		if err == nil {
			break
		}
		select {
		case serveErr := <-result:
			t.Fatalf("ServeTLS returned early: %v", serveErr)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("HTTPS request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ServeTLS() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTLS did not return")
	}
}
