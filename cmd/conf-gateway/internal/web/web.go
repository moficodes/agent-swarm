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

// Package web serves the demo's pages and static assets and guards JSON
// request bodies.
package web

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/auth"
)

const presenterKeyRequiredPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>Presenter key required</title></head>
<body style="font-family:sans-serif;padding:2rem">
<h1>Presenter key required</h1>
<p>Open this page with <code>?key=&lt;presenter key&gt;</code>. The gateway logs the full URL at startup.</p>
</body></html>
`

// Pages serves the demo's HTML pages.
type Pages struct {
	// FS holds the page files. Pages answer 500 when it is nil.
	FS fs.FS
	// PresenterKey unlocks presenter pages.
	PresenterKey string
}

// Page serves a static HTML file on the given paths. A presenter page first
// requires the presenter key: a correct ?key= query sets the presenter cookie
// and redirects to the same path without the key.
func (p Pages) Page(file string, presenter bool, paths ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(paths, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if presenter {
			if key := r.URL.Query().Get("key"); key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(p.PresenterKey)) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name:     auth.PresenterCookieName,
					Value:    p.PresenterKey,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
				return
			}
			if !auth.PresenterOK(r, p.PresenterKey) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, presenterKeyRequiredPage)
				return
			}
		}
		if p.FS == nil {
			http.Error(w, "Static filesystem not configured", http.StatusInternalServerError)
			return
		}
		data, err := fs.ReadFile(p.FS, file)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read %s: %v", file, err), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}
}

// NormalizeStaticFS returns the static/ subtree of sysFS when it has one, so
// callers may pass either the embedded tree or the directory itself.
func NormalizeStaticFS(sysFS fs.FS) fs.FS {
	if sysFS == nil {
		return nil
	}
	if _, err := fs.Stat(sysFS, "static/chat.html"); err == nil {
		if sub, err := fs.Sub(sysFS, "static"); err == nil {
			return sub
		}
	}
	return sysFS
}

// ResolveStaticFS finds the static directory on disk, for runs without an
// embedded filesystem. It returns nil when none is found.
func ResolveStaticFS() fs.FS {
	_, currentFile, _, ok := runtime.Caller(0)
	if ok {
		devPath := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "static"))
		if fi, err := os.Stat(devPath); err == nil && fi.IsDir() {
			if _, err := os.Stat(filepath.Join(devPath, "chat.html")); err == nil {
				return os.DirFS(devPath)
			}
		}
	}

	candidates := []string{
		"static",
		"../static",
		"../../static",
		"demos/conference/cmd/conf-gateway/static",
		"cmd/conf-gateway/static",
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			if _, err := os.Stat(filepath.Join(c, "chat.html")); err == nil {
				return os.DirFS(c)
			}
		}
	}
	return nil
}

// MaxRequestBodyBytes caps JSON request bodies accepted by the gateway.
const MaxRequestBodyBytes = 64 << 10

// JSONPOST requires POST bodies to be JSON no larger than MaxRequestBodyBytes.
// The body is buffered so downstream handlers and the proxy can re-read it.
func JSONPOST(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next(w, r)
	}
}
