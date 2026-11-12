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

// Package routertls builds an mTLS HTTP client for talking to atenet-router
// using a projected Kubernetes PodCertificate credential bundle and a
// servicedns ClusterTrustBundle CA file.
package routertls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
)

// NewHTTPClient returns an *http.Client configured for mTLS to atenet-router
// when caFile and credBundle are set, or (nil, nil) when both are empty.
// Setting only one of the two returns an error. Both files are validated once
// up front and re-read whenever their stat changes on subsequent handshakes.
func NewHTTPClient(caFile, credBundle string) (*http.Client, error) {
	if caFile == "" && credBundle == "" {
		return nil, nil
	}
	if caFile == "" || credBundle == "" {
		return nil, errors.New("both ROUTER_CA_FILE and ROUTER_CRED_BUNDLE must be set together")
	}
	loadPool := newPoolCache(caFile)
	if _, err := loadPool(); err != nil {
		return nil, fmt.Errorf("loading router CA bundle %q: %w", caFile, err)
	}
	loadCert := newCertCache(credBundle)
	if _, err := loadCert(nil); err != nil {
		return nil, fmt.Errorf("loading router client credential bundle %q: %w", credBundle, err)
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tr := base.Clone()
	tr.ForceAttemptHTTP2 = true
	tr.MaxIdleConns = 4096
	tr.MaxIdleConnsPerHost = 4096
	tr.MaxConnsPerHost = 0
	tr.TLSClientConfig = &tls.Config{
		MinVersion: getMinTLSVersion(),
		// Chain and hostname verification run in VerifyConnection against the
		// reloadable root pool so CA rotations apply without a restart.
		InsecureSkipVerify:   true, //nolint:gosec
		GetClientCertificate: loadCert,
		VerifyConnection: func(cs tls.ConnectionState) error {
			pool, err := loadPool()
			if err != nil {
				return err
			}
			if len(cs.PeerCertificates) == 0 {
				return errors.New("router presented no certificate")
			}
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			_, err = cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:         pool,
				Intermediates: inter,
				DNSName:       cs.ServerName,
			})
			return err
		},
	}
	return &http.Client{Transport: tr}, nil
}

func getMinTLSVersion() uint16 {
	return tls.VersionTLS13
}

type certCache struct {
	path string
	mu   sync.Mutex
	fi   os.FileInfo
	cert *tls.Certificate
}

func newCertCache(path string) func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	c := &certCache{path: path}
	return func(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		c.mu.Lock()
		defer c.mu.Unlock()

		fi, err := os.Stat(c.path)
		if err != nil {
			return nil, fmt.Errorf("stat credential bundle %q: %w", c.path, err)
		}
		if c.cert != nil && os.SameFile(c.fi, fi) && fi.ModTime().Equal(c.fi.ModTime()) && fi.Size() == c.fi.Size() {
			return c.cert, nil
		}
		cert, err := parseCredBundle(c.path)
		if err != nil {
			return nil, err
		}
		c.fi, c.cert = fi, cert
		return cert, nil
	}
}

type poolCache struct {
	path string
	mu   sync.Mutex
	fi   os.FileInfo
	pool *x509.CertPool
}

func newPoolCache(path string) func() (*x509.CertPool, error) {
	c := &poolCache{path: path}
	return func() (*x509.CertPool, error) {
		c.mu.Lock()
		defer c.mu.Unlock()

		fi, err := os.Stat(c.path)
		if err != nil {
			return nil, fmt.Errorf("stat trust bundle %q: %w", c.path, err)
		}
		if c.pool != nil && os.SameFile(c.fi, fi) && fi.ModTime().Equal(c.fi.ModTime()) && fi.Size() == c.fi.Size() {
			return c.pool, nil
		}
		pemBytes, err := os.ReadFile(c.path)
		if err != nil {
			return nil, fmt.Errorf("reading trust bundle %q: %w", c.path, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("trust bundle %q contains no certificates", c.path)
		}
		c.fi, c.pool = fi, pool
		return pool, nil
	}
}

func parseCredBundle(path string) (*tls.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading credential bundle: %w", err)
	}
	var keyBytes []byte
	var chain [][]byte
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			chain = append(chain, block.Bytes)
		case "PRIVATE KEY":
			keyBytes = block.Bytes
		default:
			return nil, fmt.Errorf("unexpected PEM block type %q", block.Type)
		}
	}
	if keyBytes == nil {
		return nil, errors.New("no PRIVATE KEY block found")
	}
	if len(chain) == 0 {
		return nil, errors.New("no CERTIFICATE blocks found")
	}
	privKey, err := x509.ParsePKCS8PrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing PKCS#8 private key: %w", err)
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, fmt.Errorf("parsing leaf certificate: %w", err)
	}
	return &tls.Certificate{
		Certificate: chain,
		Leaf:        leaf,
		PrivateKey:  privKey,
	}, nil
}
