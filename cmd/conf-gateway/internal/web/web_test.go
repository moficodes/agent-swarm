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

package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/auth"
)

func TestResolveStaticFS_FindsStaticDir(t *testing.T) {
	sfs := ResolveStaticFS()
	if sfs == nil {
		t.Fatal("ResolveStaticFS found no static directory")
	}
	if _, err := fs.Stat(sfs, "chat.html"); err != nil {
		t.Fatalf("static directory has no chat.html: %v", err)
	}
}

func TestNormalizeStaticFS(t *testing.T) {
	nested := fstest.MapFS{"static/chat.html": {Data: []byte("chat")}}
	if _, err := fs.Stat(NormalizeStaticFS(nested), "chat.html"); err != nil {
		t.Errorf("nested tree not unwrapped: %v", err)
	}
	flat := fstest.MapFS{"chat.html": {Data: []byte("chat")}}
	if _, err := fs.Stat(NormalizeStaticFS(flat), "chat.html"); err != nil {
		t.Errorf("flat tree changed: %v", err)
	}
	if NormalizeStaticFS(nil) != nil {
		t.Error("NormalizeStaticFS(nil) != nil")
	}
}

func TestPage_PresenterGate(t *testing.T) {
	p := Pages{FS: fstest.MapFS{"stage.html": {Data: []byte("stage")}, "chat.html": {Data: []byte("chat")}}, PresenterKey: "pk"}
	stage := p.Page("stage.html", true, "/stage")
	chat := p.Page("chat.html", false, "/chat")

	serve := func(h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, r)
		return rec
	}

	if rec := serve(chat, httptest.NewRequest(http.MethodGet, "/chat", nil)); rec.Code != http.StatusOK || rec.Body.String() != "chat" {
		t.Errorf("chat: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(chat, httptest.NewRequest(http.MethodGet, "/other", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: %d", rec.Code)
	}
	if rec := serve(stage, httptest.NewRequest(http.MethodGet, "/stage", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("stage without key: %d", rec.Code)
	} else if body := rec.Body.String(); !strings.Contains(body, "PRESENTER_TOKEN") || !strings.Contains(body, "conf-gateway Secret") {
		// The URL is logged only when the gateway generated its own key, so
		// the page must say where a configured key lives.
		t.Errorf("key-required page does not say where the key comes from: %q", body)
	}
	if rec := serve(stage, httptest.NewRequest(http.MethodGet, "/stage?key=wrong", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("stage with wrong key: %d", rec.Code)
	}
	rec := serve(stage, httptest.NewRequest(http.MethodGet, "/stage?key=pk", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/stage" {
		t.Fatalf("stage with key: %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.PresenterCookieName || !cookies[0].HttpOnly {
		t.Fatalf("presenter cookie = %+v", cookies)
	}
	withCookie := httptest.NewRequest(http.MethodGet, "/stage", nil)
	withCookie.AddCookie(cookies[0])
	if rec := serve(stage, withCookie); rec.Code != http.StatusOK || rec.Body.String() != "stage" {
		t.Errorf("stage with cookie: %d %q", rec.Code, rec.Body.String())
	}
}

func TestJSONPOST(t *testing.T) {
	h := JSONPOST(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	})
	post := func(ct, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	if rec := post("application/json; charset=utf-8", `{"a":1}`); rec.Code != http.StatusOK || rec.Body.String() != `{"a":1}` {
		t.Errorf("json body: %d %q", rec.Code, rec.Body.String())
	}
	if rec := post("text/plain", `{}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", rec.Code)
	}
	if rec := post("application/json", strings.Repeat("x", MaxRequestBodyBytes+1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}
}
