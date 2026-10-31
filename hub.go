package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"sync"
)

type tunnelHub struct {
	mu            sync.Mutex
	sessions      map[string]*webManager
	token, binary string
	draft         *webManager
}

func newTunnelHub(token, binary string) *tunnelHub {
	h := &tunnelHub{sessions: make(map[string]*webManager), token: token, binary: binary, draft: &webManager{state: "stopped", token: token, binary: binary}}
	entries, err := readConnections()
	if err != nil {
		return h
	}
	legacyID, _ := loadWebPreferences()
	for _, entry := range entries {
		if !tunnelPattern.MatchString(entry.Tunnel) {
			continue
		}
		m := h.newSession(entry.Tunnel)
		m.workspace = entry.Workspace
		if runtime.GOOS == "darwin" {
			m.key, _ = loadKeychainAccount(entry.Tunnel)
			m.keyStored = m.key != ""
			if m.key == "" && entry.Tunnel == legacyID {
				m.key, _ = loadKeychainKey()
			}
		}
		h.sessions[entry.Tunnel] = m
	}
	return h
}
func (h *tunnelHub) newSession(id string) *webManager {
	m := &webManager{state: "stopped", token: h.token, binary: h.binary, tunnel: id}
	if runtime.GOOS == "darwin" {
		m.authenticate = authenticateDevice
		m.saveKey = func(key string) error { return saveKeychainAccount(id, key) }
	}
	m.saveSettings = func(tunnel, workspace string) error {
		return updateConnections(savedPreferences{Tunnel: tunnel, Workspace: workspace}, false)
	}
	return m
}
func (h *tunnelHub) stopAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.sessions {
		m.mu.Lock()
		m.generation++
		if m.cmd != nil && m.cmd.Process != nil {
			_ = m.cmd.Process.Kill()
		}
		if m.auditPath != "" {
			_ = os.Remove(m.auditPath)
		}
		m.mu.Unlock()
	}
}
func (h *tunnelHub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || host != "127.0.0.1" {
			http.Error(w, "Forbidden", 403)
			return
		}
		if r.URL.Path == "/api/connections" {
			if r.Method != "GET" {
				http.Error(w, "Method not allowed", 405)
				return
			}
			h.mu.Lock()
			list := make([]*webManager, 0, len(h.sessions))
			for _, m := range h.sessions {
				list = append(list, m)
			}
			h.mu.Unlock()
			type item struct {
				Tunnel    string `json:"tunnel"`
				Workspace string `json:"workspace"`
				State     string `json:"state"`
			}
			var items = []item{}
			for _, m := range list {
				m.mu.Lock()
				if m.workspace != "" {
					items = append(items, item{m.tunnel, m.workspace, m.state})
				}
				m.mu.Unlock()
			}
			sort.Slice(items, func(i, j int) bool { return items[i].Tunnel < items[j].Tunnel })
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(items)
			return
		}
		if r.URL.Path == "/api/start" {
			if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != h.token {
				http.Error(w, "Forbidden", 403)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
			if err != nil {
				http.Error(w, "Invalid request", 400)
				return
			}
			var s webSettings
			if json.Unmarshal(body, &s) != nil || !tunnelPattern.MatchString(s.Tunnel) {
				http.Error(w, "Invalid tunnel ID", 400)
				return
			}
			h.mu.Lock()
			m := h.sessions[s.Tunnel]
			if m == nil {
				m = h.newSession(s.Tunnel)
				h.sessions[s.Tunnel] = m
			}
			h.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
			m.handler().ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/remove" {
			if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != h.token {
				http.Error(w, "Forbidden", 403)
				return
			}
			id := r.URL.Query().Get("id")
			h.mu.Lock()
			defer h.mu.Unlock()
			m := h.sessions[id]
			if m == nil {
				http.NotFound(w, r)
				return
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.state == "running" || m.state == "starting" {
				http.Error(w, "Stop the connection before removing it.", 409)
				return
			}
			if err := updateConnections(savedPreferences{Tunnel: id}, true); err != nil {
				http.Error(w, "Could not remove connection settings.", 500)
				return
			}
			if runtime.GOOS == "darwin" && m.key != "" {
				account := id
				if !m.keyStored {
					account = keychainAccount
				}
				if err := exec.Command("/usr/bin/security", "delete-generic-password", "-s", keychainService, "-a", account).Run(); err != nil {
					http.Error(w, "Connection removed, but could not delete its Keychain entry.", 500)
					delete(h.sessions, id)
					if m.auditPath != "" {
						_ = os.Remove(m.auditPath)
					}
					return
				}
			}
			delete(h.sessions, id)
			if m.auditPath != "" {
				_ = os.Remove(m.auditPath)
			}
			w.WriteHeader(204)
			return
		}
		id := r.URL.Query().Get("id")
		h.mu.Lock()
		m := h.sessions[id]
		h.mu.Unlock()
		if m == nil {
			if id != "" && (r.URL.Path == "/api/stop" || r.URL.Path == "/api/reveal-key" || r.URL.Path == "/api/logs" || r.URL.Path == "/api/usage") {
				http.NotFound(w, r)
				return
			}
			m = h.draft
		}
		m.handler().ServeHTTP(w, r)
	})
}
