package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestMultipleTunnelsRunAndStopIndependently(t *testing.T) {
	dir := t.TempDir()
	client := filepath.Join(dir, "tunnel-client")
	if err := os.WriteFile(client, []byte("#!/bin/sh\nif [ \"$1\" = run ]; then exec sleep 30; fi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := &tunnelHub{sessions: make(map[string]*webManager), token: "csrf", binary: "/tmp/example", draft: &webManager{token: "csrf", state: "stopped"}}
	for _, id := range []string{"tunnel_one", "tunnel_two"} {
		m := h.newSession(id)
		m.saveKey = nil
		m.saveSettings = func(string, string) error { return nil }
		h.sessions[id] = m
	}
	defer h.stopAll()
	call := func(path string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:1234"+path, bytes.NewReader(body))
		r.Header.Set("X-Workspace-Token", "csrf")
		w := httptest.NewRecorder()
		h.handler().ServeHTTP(w, r)
		return w
	}
	for _, id := range []string{"tunnel_one", "tunnel_two"} {
		data, _ := json.Marshal(webSettings{Tunnel: id, Key: "key-" + id, Workspace: dir})
		if w := call("/api/start", data); w.Code != http.StatusAccepted {
			t.Fatalf("start %s: %d %s", id, w.Code, w.Body)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		both := true
		for _, m := range h.sessions {
			m.mu.Lock()
			both = both && m.state == "running"
			m.mu.Unlock()
		}
		if both {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connections did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w := call("/api/stop?id=tunnel_one", nil); w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d", w.Code)
	}
	m := h.sessions["tunnel_two"]
	m.mu.Lock()
	state, key := m.state, m.key
	m.mu.Unlock()
	if state != "running" || key != "key-tunnel_two" {
		t.Fatal("stopping one tunnel affected the other")
	}
}

func TestConnectionsMigrateLegacyAndPersistSeparately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveWebPreferences("tunnel_old", "/tmp/old"); err != nil {
		t.Fatal(err)
	}
	entries, err := readConnections()
	if err != nil || len(entries) != 1 || entries[0].Tunnel != "tunnel_old" {
		t.Fatalf("legacy settings not loaded: %+v %v", entries, err)
	}
	if err := updateConnections(savedPreferences{Tunnel: "tunnel_new", Workspace: "/tmp/new"}, false); err != nil {
		t.Fatal(err)
	}
	entries, err = readConnections()
	if err != nil || len(entries) != 2 {
		t.Fatalf("connections not saved: %+v %v", entries, err)
	}
	for _, id := range []string{"tunnel_old", "tunnel_new"} {
		if err := updateConnections(savedPreferences{Tunnel: id}, true); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = readConnections()
	if err != nil || len(entries) != 0 {
		t.Fatalf("removed legacy connection returned: %+v %v", entries, err)
	}
}

func TestMCPLogProcessHelper(t *testing.T) {
	if os.Getenv("WORKSPACE_TEST_HELPER") != "1" {
		return
	}
	main()
	os.Exit(0)
}
func TestMCPRequestResponseLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPLogProcessHelper$")
	cmd.Env = append(os.Environ(), "WORKSPACE_TEST_HELPER=1", "WORKSPACE_ROOT="+dir, "WORKSPACE_AUDIT_LOG="+path)
	cmd.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"list_dir\",\"arguments\":{\"path\":\".\"}}}\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("server: %s %v", out, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]json.RawMessage
	if err = json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(entry["request"], []byte("list_dir")) || !bytes.Contains(entry["response"], []byte("result")) {
		t.Fatalf("incomplete log: %s", data)
	}
}
func TestLogEndpointRequiresToken(t *testing.T) {
	m := &webManager{token: "secret"}
	r := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/logs", nil)
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d", w.Code)
	}
}

func TestWebDisplaysCurrentVersion(t *testing.T) {
	m := &webManager{token: "secret"}
	r := httptest.NewRequest("GET", "http://127.0.0.1:1234/", nil)
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "v"+appVersion) || strings.Contains(w.Body.String(), "__APP_VERSION__") {
		t.Fatal("current version is missing from page")
	}
	result := (&server{}).handle(jsonrpcRequest{Method: "initialize"}).(map[string]any)
	if result["serverInfo"].(map[string]any)["version"] != appVersion {
		t.Fatal("MCP server version differs from app")
	}
}

func TestConnectionNamePersistsAndCanBeChanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := updateConnections(savedPreferences{Tunnel: "tunnel_named", Workspace: t.TempDir(), Name: "First"}, false); err != nil {
		t.Fatal(err)
	}
	h := newTunnelHub("csrf", "/tmp/example")
	r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/rename?id=tunnel_named", strings.NewReader(`{"name":"  Second  "}`))
	r.Header.Set("X-Workspace-Token", "csrf")
	w := httptest.NewRecorder()
	h.handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	entries, err := readConnections()
	if err != nil || len(entries) != 1 || entries[0].Name != "Second" {
		t.Fatalf("name not persisted: %+v %v", entries, err)
	}
	if got := newTunnelHub("csrf", "/tmp/example").sessions["tunnel_named"].name; got != "Second" {
		t.Fatalf("name not restored: %q", got)
	}
}
