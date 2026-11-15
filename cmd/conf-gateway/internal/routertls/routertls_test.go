// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package routertls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewHTTPClient_DisabledWhenUnset(t *testing.T) {
	c, err := NewHTTPClient("", "")
	if err != nil || c != nil {
		t.Fatalf("NewHTTPClient(\"\", \"\") = %v, %v; want nil, nil", c, err)
	}
}

func TestNewHTTPClient_RequiresBothFiles(t *testing.T) {
	if _, err := NewHTTPClient("/some/ca.pem", ""); err == nil {
		t.Fatal("expected error when only caFile is set")
	}
	if _, err := NewHTTPClient("", "/some/cred.pem"); err == nil {
		t.Fatal("expected error when only credBundle is set")
	}
}

func TestNewHTTPClient_MTLSHandshakeAndRotation(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "router-ca.pem")
	credFile := filepath.Join(dir, "credential-bundle.pem")

	serverCACert, serverCAKey := mustSelfSignedCA(t, "servicedns-ca")
	clientCACert, clientCAKey := mustSelfSignedCA(t, "podidentity-ca")

	serverTLSCert := mustIssueLeaf(t, serverCACert, serverCAKey, "atenet-router.ate-system.svc", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	writeTrustBundle(t, caFile, serverCACert)
	writeCredBundle(t, credFile, clientCACert, clientCAKey, "spiffe://cluster.local/ns/ate-demo-conf/sa/conf-gateway-v1")

	clientCAPool := x509.NewCertPool()
	clientCAPool.AddCert(clientCACert)

	var seenURI string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 && len(r.TLS.PeerCertificates[0].URIs) > 0 {
			seenURI = r.TLS.PeerCertificates[0].URIs[0].String()
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverTLSCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAPool,
	}
	srv.StartTLS()
	defer srv.Close()

	client, err := NewHTTPClient(caFile, credFile)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	// Dial the test server's IP while verifying the router's DNS SAN.
	tr := client.Transport.(*http.Transport)
	tr.TLSClientConfig.ServerName = "atenet-router.ate-system.svc"

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	_ = resp.Body.Close()
	if seenURI != "spiffe://cluster.local/ns/ate-demo-conf/sa/conf-gateway-v1" {
		t.Fatalf("seenURI = %q, want v1 SPIFFE ID", seenURI)
	}

	// Rotate the client credential bundle in place via atomic rename and verify
	// the next fresh connection presents the rotated certificate.
	tr.CloseIdleConnections()
	writeCredBundle(t, credFile, clientCACert, clientCAKey, "spiffe://cluster.local/ns/ate-demo-conf/sa/conf-gateway-v2")
	resp, err = client.Get(srv.URL)
	if err != nil {
		t.Fatalf("second GET after rotation: %v", err)
	}
	_ = resp.Body.Close()
	if seenURI != "spiffe://cluster.local/ns/ate-demo-conf/sa/conf-gateway-v2" {
		t.Fatalf("seenURI after rotation = %q, want v2 SPIFFE ID", seenURI)
	}
}

func mustSelfSignedCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func mustIssueLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, dnsSAN string, eku []x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     []string{dnsSAN},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		Leaf:        leaf,
		PrivateKey:  key,
	}
}

func writeTrustBundle(t *testing.T, path string, ca *x509.Certificate) {
	t.Helper()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCredBundle(t *testing.T, path string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, spiffeURI string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(spiffeURI)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{u},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	// PodCertificate credential bundles place PRIVATE KEY first, then CERTIFICATE blocks.
	var buf []byte
	buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})...)
	buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestNewHTTPClient_DisablesHTTP2ForServiceLoadBalancing(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "router-ca.pem")
	credFile := filepath.Join(dir, "credential-bundle.pem")

	serverCACert, _ := mustSelfSignedCA(t, "servicedns-ca")
	clientCACert, clientCAKey := mustSelfSignedCA(t, "podidentity-ca")
	writeTrustBundle(t, caFile, serverCACert)
	writeCredBundle(t, credFile, clientCACert, clientCAKey, "spiffe://cluster.local/ns/ate-demo-conf/sa/conf-gateway")

	client, err := NewHTTPClient(caFile, credFile)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if tr.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want false so ClusterIP router connections spread across HTTP/1.1 sockets instead of wedging on a shared HTTP/2 connection")
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("ResponseHeaderTimeout <= 0, want a bounded header timeout")
	}
}

func TestNewHTTPClient_EndpointResolverRoundRobinsRouterPods(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "router-ca.pem")
	credFile := filepath.Join(dir, "credential-bundle.pem")

	serverCACert, serverCAKey := mustSelfSignedCA(t, "servicedns-ca")
	clientCACert, clientCAKey := mustSelfSignedCA(t, "podidentity-ca")
	serverTLSCert := mustIssueLeaf(t, serverCACert, serverCAKey, "atenet-router.ate-system.svc", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	writeTrustBundle(t, caFile, serverCACert)
	writeCredBundle(t, credFile, clientCACert, clientCAKey, "spiffe://cluster.local/ns/ate-demo-conf/sa/conf-gateway")

	clientCAPool := x509.NewCertPool()
	clientCAPool.AddCert(clientCACert)

	hits := make([]int, 2)
	var addrs []string
	for idx := 0; idx < 2; idx++ {
		i := idx
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[i]++
			w.WriteHeader(http.StatusOK)
		}))
		srv.TLS = &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{serverTLSCert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    clientCAPool,
		}
		srv.StartTLS()
		defer srv.Close()
		addrs = append(addrs, srv.Listener.Addr().String())
	}

	client, err := NewHTTPClient(caFile, credFile, WithEndpointResolver(func(_ context.Context) ([]string, error) {
		return addrs, nil
	}))
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	tr := client.Transport.(*http.Transport)
	tr.DisableKeepAlives = true

	for i := 0; i < 4; i++ {
		resp, err := client.Get("https://atenet-router.ate-system.svc:443/healthz")
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		_ = resp.Body.Close()
	}
	if hits[0] != 2 || hits[1] != 2 {
		t.Fatalf("hits = %v, want [2 2] across both router endpoints", hits)
	}
}
