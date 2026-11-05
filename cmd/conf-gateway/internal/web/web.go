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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
)

const presenterKeyRequiredPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>Presenter key required</title></head>
<body style="font-family:sans-serif;padding:2rem">
<h1>Presenter key required</h1>
<p>Open this page with <code>?key=&lt;presenter key&gt;</code>.</p>
<p>The key is the gateway's <code>PRESENTER_TOKEN</code>; the install script stores it in the conf-gateway Secret. A gateway started without one generates a key and logs the full URL at startup.</p>
</body></html>
`

var googleSignInPageTmpl = template.Must(template.New("google-signin").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Presenter Sign-In — Agent Substrate</title>
<style>
  :root {
    --bg: #0b0f19;
    --card: #111827;
    --border: #1f2937;
    --text: #f9fafb;
    --muted: #9ca3af;
    --accent: #3b82f6;
    --danger-bg: rgba(239, 68, 68, 0.12);
    --danger-border: rgba(239, 68, 68, 0.35);
    --danger-text: #fca5a5;
    --chip-bg: #1e293b;
    --chip-border: #334155;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 1.5rem;
    background: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  }
  .auth-card {
    width: 100%;
    max-width: 440px;
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 14px;
    padding: 2rem;
    box-shadow: 0 20px 40px rgba(0, 0, 0, 0.45);
  }
  .brand-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 1.25rem;
  }
  .brand-icon {
    width: 36px;
    height: 36px;
    border-radius: 8px;
    background: rgba(59, 130, 246, 0.15);
    border: 1px solid rgba(59, 130, 246, 0.35);
    display: flex;
    align-items: center;
    justify-content: center;
    color: #60a5fa;
    flex-shrink: 0;
  }
  h1 {
    font-size: 1.25rem;
    font-weight: 600;
    margin: 0;
  }
  .subtitle {
    color: var(--muted);
    font-size: 0.9rem;
    line-height: 1.5;
    margin: 0 0 1.25rem 0;
  }
  .domains-label {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted);
    margin-bottom: 0.5rem;
  }
  .domain-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin-bottom: 1.5rem;
  }
  .domain-chip {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.78rem;
    padding: 0.25rem 0.55rem;
    border-radius: 6px;
    background: var(--chip-bg);
    border: 1px solid var(--chip-border);
    color: #e2e8f0;
  }
  .google-btn {
    width: 100%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.65rem;
    padding: 0.75rem 1rem;
    border-radius: 8px;
    border: 1px solid #dadce0;
    background: #ffffff;
    color: #1f2937;
    font-size: 0.95rem;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.15s ease, transform 0.05s ease;
  }
  .google-btn:hover { background: #f8fafc; }
  .google-btn:active { transform: translateY(1px); }
  .google-btn:disabled { opacity: 0.6; cursor: wait; }
  #gsi-container {
    display: flex;
    justify-content: center;
    margin-top: 0.5rem;
  }
  .error-box {
    display: none;
    margin-top: 1rem;
    padding: 0.75rem 0.9rem;
    border-radius: 8px;
    background: var(--danger-bg);
    border: 1px solid var(--danger-border);
    color: var(--danger-text);
    font-size: 0.85rem;
    line-height: 1.4;
  }
  details.key-fallback {
    margin-top: 1.5rem;
    padding-top: 1rem;
    border-top: 1px solid var(--border);
    font-size: 0.82rem;
    color: var(--muted);
  }
  details.key-fallback summary {
    cursor: pointer;
    user-select: none;
  }
  .key-form {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.65rem;
  }
  .key-input {
    flex: 1;
    padding: 0.5rem 0.65rem;
    border-radius: 6px;
    border: 1px solid var(--chip-border);
    background: var(--bg);
    color: var(--text);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.82rem;
  }
  .key-submit {
    padding: 0.5rem 0.85rem;
    border-radius: 6px;
    border: 1px solid var(--chip-border);
    background: var(--chip-bg);
    color: var(--text);
    font-size: 0.82rem;
    font-weight: 500;
    cursor: pointer;
  }
</style>
</head>
<body>
<main class="auth-card">
  <div class="brand-row">
    <div class="brand-icon" aria-hidden="true">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect>
        <path d="M7 11V7a5 5 0 0 1 10 0v4"></path>
      </svg>
    </div>
    <h1>Presenter Access Required</h1>
  </div>
  <p class="subtitle">Sign in with an authorized Google Workspace account to access the conference stage dashboard and live telemetry feed.</p>
  <div class="domains-label">Allowed Google Domains</div>
  <div class="domain-chips">
    {{- range .AllowedDomains}}
    <span class="domain-chip">@{{.}}</span>
    {{- end}}
  </div>

  {{- if .FirebaseAPIKey}}
  <button id="btn-google-signin" type="button" class="google-btn">
    <svg width="18" height="18" viewBox="0 0 48 48" aria-hidden="true">
      <path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/>
      <path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/>
      <path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/>
      <path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/>
    </svg>
    <span>Sign in with Google</span>
  </button>
  {{- else if .ClientID}}
  <div id="gsi-container"></div>
  {{- end}}

  <div id="auth-error" class="error-box" role="alert"></div>

  <details class="key-fallback">
    <summary>Use presenter token instead (CLI / port-forward)</summary>
    <form class="key-form" method="get" action="">
      <input class="key-input" type="password" name="key" placeholder="PRESENTER_TOKEN" required autocomplete="off">
      <button class="key-submit" type="submit">Unlock</button>
    </form>
  </details>
</main>
<script type="module">
const errorBox = document.getElementById('auth-error');
function showError(msg) {
  errorBox.textContent = msg;
  errorBox.style.display = 'block';
}
async function exchangeToken(idToken) {
  errorBox.style.display = 'none';
  const resp = await fetch('/api/auth/google', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id_token: idToken })
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(text.trim() || ('Authentication failed (HTTP ' + resp.status + ')'));
  }
  window.location.reload();
}
const cfg = {{.ConfigJSON}};
if (cfg.firebaseApiKey) {
  const btn = document.getElementById('btn-google-signin');
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    try {
      const [{ initializeApp }, { getAuth, GoogleAuthProvider, signInWithPopup, signOut }] = await Promise.all([
        import('https://www.gstatic.com/firebasejs/11.0.1/firebase-app.js'),
        import('https://www.gstatic.com/firebasejs/11.0.1/firebase-auth.js')
      ]);
      const app = initializeApp({
        apiKey: cfg.firebaseApiKey,
        authDomain: cfg.firebaseAuthDomain
      });
      const auth = getAuth(app);
      const provider = new GoogleAuthProvider();
      provider.setCustomParameters({ prompt: 'select_account' });
      const result = await signInWithPopup(auth, provider);
      const idToken = await result.user.getIdToken();
      await exchangeToken(idToken);
      await signOut(auth).catch(() => {});
    } catch (err) {
      showError(err && err.message ? err.message : String(err));
      btn.disabled = false;
    }
  });
} else if (cfg.clientId) {
  const s = document.createElement('script');
  s.src = 'https://accounts.google.com/gsi/client';
  s.async = true;
  s.onload = () => {
    window.google.accounts.id.initialize({
      client_id: cfg.clientId,
      callback: async (response) => {
        try {
          await exchangeToken(response.credential);
        } catch (err) {
          showError(err && err.message ? err.message : String(err));
        }
      }
    });
    window.google.accounts.id.renderButton(
      document.getElementById('gsi-container'),
      { theme: 'outline', size: 'large', width: 360, text: 'signin_with' }
    );
  };
  document.head.appendChild(s);
}
</script>
</body>
</html>
`))

// Pages serves the demo's HTML pages.
type Pages struct {
	// FS holds the page files. Pages answer 500 when it is nil.
	FS fs.FS
	// PresenterKey unlocks presenter pages.
	PresenterKey string
	// GoogleAuth, when Enabled(), renders the Google Sign-In gate on
	// unauthenticated presenter pages instead of the plain presenter-key prompt.
	GoogleAuth auth.GoogleAuthConfig
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
				if p.GoogleAuth.Enabled() {
					p.renderGoogleSignInPage(w)
					return
				}
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
		data = versionStaticRefs(p.FS, data)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}
}

var staticRefPattern = regexp.MustCompile(`"/static/([a-zA-Z0-9._/-]+)"`)

// versionStaticRefs appends a content-hash ?v= query parameter to double-quoted
// /static/<asset> URLs whose target file exists in fsys, ensuring browsers bust
// stale cached JS/CSS immediately after a gateway update.
func versionStaticRefs(fsys fs.FS, html []byte) []byte {
	if fsys == nil || !bytes.Contains(html, []byte(`"/static/`)) {
		return html
	}
	return staticRefPattern.ReplaceAllFunc(html, func(match []byte) []byte {
		name := string(match[len(`"/static/`) : len(match)-1])
		asset, err := fs.ReadFile(fsys, name)
		if err != nil {
			return match
		}
		sum := sha256.Sum256(asset)
		return []byte(fmt.Sprintf(`"/static/%s?v=%x"`, name, sum[:8]))
	})
}

// StaticHandler serves static assets from fsys with strong SHA-256 ETags and
// Cache-Control: no-cache so browsers revalidate embedded assets cleanly across
// gateway rollouts instead of relying on heuristic caching.
func StaticHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fsys != nil {
			clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
			if clean != "" {
				if data, err := fs.ReadFile(fsys, clean); err == nil {
					sum := sha256.Sum256(data)
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sum[:8]))
				}
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (p Pages) renderGoogleSignInPage(w http.ResponseWriter) {
	domains := auth.NormalizeDomains(p.GoogleAuth.AllowedDomains)
	authDomain := p.GoogleAuth.FirebaseAuthDomain
	if authDomain == "" && p.GoogleAuth.FirebaseProjectID != "" {
		authDomain = p.GoogleAuth.FirebaseProjectID + ".firebaseapp.com"
	}
	cfgJSON, _ := json.Marshal(map[string]string{
		"clientId":           p.GoogleAuth.ClientID,
		"firebaseApiKey":     p.GoogleAuth.FirebaseAPIKey,
		"firebaseAuthDomain": authDomain,
	})
	_ = googleSignInPageTmpl.Execute(w, struct {
		AllowedDomains []string
		ClientID       string
		FirebaseAPIKey string
		ConfigJSON     template.JS
	}{
		AllowedDomains: domains,
		ClientID:       p.GoogleAuth.ClientID,
		FirebaseAPIKey: p.GoogleAuth.FirebaseAPIKey,
		ConfigJSON:     template.JS(cfgJSON),
	})
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
