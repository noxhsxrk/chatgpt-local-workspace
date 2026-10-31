package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebRejectsUntrustedRequests(t *testing.T) {
	m := &webManager{state: "stopped", token: "secret"}
	for _, tc := range []struct {
		host, token string
		want        int
	}{
		{"127.0.0.1:1234", "", http.StatusForbidden},
		{"evil.example:1234", "secret", http.StatusForbidden},
		{"127.0.0.1:1234", "secret", http.StatusNoContent},
	} {
		r := httptest.NewRequest("POST", "http://"+tc.host+"/api/stop", nil)
		r.Header.Set("X-Workspace-Token", tc.token)
		w := httptest.NewRecorder()
		m.handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("host %s: got %d, want %d", tc.host, w.Code, tc.want)
		}
	}
}
func TestWebRejectsInvalidSettings(t *testing.T) {
	m := &webManager{state: "stopped", token: "secret"}
	r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/start", strings.NewReader(`{"tunnel":"invalid","key":"secret","workspace":"/tmp"}`))
	r.Header.Set("X-Workspace-Token", "secret")
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestWebStartStopWithTunnelClient(t *testing.T) {
	dir := t.TempDir()
	client := filepath.Join(dir, "tunnel-client")
	if err := os.WriteFile(client, []byte("#!/bin/sh\nif [ \"$1\" = run ]; then exec sleep 30; fi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := &webManager{state: "stopped", token: "secret", binary: "/tmp/example"}
	data, _ := json.Marshal(webSettings{Tunnel: "tunnel_123", Key: "private-key", Workspace: dir})
	r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/start", bytes.NewReader(data))
	r.Header.Set("X-Workspace-Token", "secret")
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		state := m.state
		m.mu.Unlock()
		if state == "running" {
			break
		}
		if state == "error" || time.Now().After(deadline) {
			t.Fatalf("unexpected state: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:1234/api/stop", nil)
	r.Header.Set("X-Workspace-Token", "secret")
	w = httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d", w.Code)
	}
	m.mu.Lock()
	if m.state != "stopped" || m.cmd != nil {
		m.mu.Unlock()
		t.Fatal("tunnel was not stopped")
	}
	m.mu.Unlock()
	r = httptest.NewRequest("GET", "http://127.0.0.1:1234/api/status", nil)
	w = httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "private-key") {
		t.Fatal("API key exposed in status")
	}
	data, _ = json.Marshal(webSettings{Tunnel: "tunnel_123", Workspace: dir})
	r = httptest.NewRequest("POST", "http://127.0.0.1:1234/api/start", bytes.NewReader(data))
	r.Header.Set("X-Workspace-Token", "secret")
	w = httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("restart without key: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:1234/api/stop", nil)
	r.Header.Set("X-Workspace-Token", "secret")
	m.handler().ServeHTTP(httptest.NewRecorder(), r)
}

func TestDiagnosticsRedactSecrets(t *testing.T) {
	var log limitedLog
	_, _ = log.Write([]byte("failed with arbitrary-private-key and Bearer sk-other-secret"))
	got := log.safeMessage("arbitrary-private-key")
	if strings.Contains(got, "arbitrary-private-key") || strings.Contains(got, "sk-other-secret") {
		t.Fatalf("secret leaked: %s", got)
	}
}

func TestRevealRequiresSuccessfulAuthentication(t *testing.T) {
	m := &webManager{token: "csrf", key: "saved-secret", authenticate: func() error { return errors.New("cancelled") }}
	reveal := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/reveal-key", nil)
		r.Header.Set("X-Workspace-Token", token)
		w := httptest.NewRecorder()
		m.handler().ServeHTTP(w, r)
		return w
	}
	for _, w := range []*httptest.ResponseRecorder{reveal("bad"), reveal("csrf")} {
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "saved-secret") {
			t.Fatalf("authentication failure leaked key: %d %s", w.Code, w.Body)
		}
	}
	m.authenticate = func() error { return nil }
	w := reveal("csrf")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "saved-secret") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authenticated reveal failed")
	}
}

func TestEmbeddedGuideImages(t *testing.T) {
	m := &webManager{token: "secret"}
	for _, path := range []string{"/assets/create_tunnel_menu.png", "/assets/create_api_key_menu.png", "/assets/create_custom_mcp.png"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:1234"+path, nil)
		w := httptest.NewRecorder()
		m.handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/png") || w.Body.Len() == 0 {
			t.Fatalf("image %s: status %d, content type %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
}
