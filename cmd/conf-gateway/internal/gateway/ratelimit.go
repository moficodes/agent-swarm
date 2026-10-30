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

package gateway

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// WithChatRateLimitRPM sets the per-attendee rate limit (requests per minute)
// on POST /api/chat. Zero or negative disables the limit.
func WithChatRateLimitRPM(rpm int) Option {
	return func(g *Gateway) {
		g.chatRateLimitRPM = rpm
	}
}

// WithSessionRateLimitRPM sets the per-IP rate limit (requests per minute) on
// GET /api/session for non-presenter requests. Zero or negative disables the limit.
func WithSessionRateLimitRPM(rpm int) Option {
	return func(g *Gateway) {
		g.sessionRateLimitRPM = rpm
	}
}

// rateLimiter enforces a sliding-window limit per key.
type rateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time
	mu     sync.Mutex
	hits   map[string][]time.Time
}

func newRateLimiter(rpm int, now func() time.Time) *rateLimiter {
	if rpm <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		limit:  rpm,
		window: time.Minute,
		now:    now,
		hits:   make(map[string][]time.Time),
	}
}

func (l *rateLimiter) allow(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)

	if len(l.hits) > 1024 {
		for k, ts := range l.hits {
			kept := ts[:0]
			for _, t := range ts {
				if t.After(cutoff) {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				delete(l.hits, k)
			} else {
				l.hits[k] = kept
			}
		}
	}

	ts := l.hits[key]
	kept := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

func (l *rateLimiter) reset() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits = make(map[string][]time.Time)
}

var (
	gfeRange1 = netip.MustParsePrefix("35.191.0.0/16")
	gfeRange2 = netip.MustParsePrefix("130.211.0.0/22")
)

func isTrustedProxyPeer(host string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		gfeRange1.Contains(addr) || gfeRange2.Contains(addr)
}

// clientIP extracts the client IP address for rate limiting. When the immediate
// peer is a trusted load balancer / local proxy and X-Forwarded-For is present,
// it uses the client IP recorded by the load balancer.
func clientIP(r *http.Request) string {
	remoteHost := strings.TrimSpace(r.RemoteAddr)
	if h, _, err := net.SplitHostPort(remoteHost); err == nil {
		remoteHost = h
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" && isTrustedProxyPeer(remoteHost) {
		var parts []string
		for _, p := range strings.Split(xff, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		// Google Cloud HTTP(S) Load Balancer appends "<client-ip>, <lb-ip>".
		if len(parts) >= 2 {
			return parts[len(parts)-2]
		}
		if len(parts) == 1 {
			return parts[0]
		}
	}
	if remoteHost == "" {
		return "unknown"
	}
	return remoteHost
}
