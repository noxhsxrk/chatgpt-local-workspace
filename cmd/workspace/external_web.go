package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

var externalSettingsMu sync.Mutex

func deleteExternalToken(account string) error {
	if !tokenAccountPattern.MatchString(account) {
		return errors.New("invalid token account")
	}
	return exec.Command("/usr/bin/security", "delete-generic-password", "-s", externalKeychainService, "-a", account).Run()
}
func registerExternalRoutes(mux *http.ServeMux, m *webManager) {
	saveToken := m.externalSaveToken
	deleteToken := m.externalDeleteToken
	if runtime.GOOS == "darwin" {
		if saveToken == nil {
			saveToken = func(account, token string) error { return saveKeychainEntry(externalKeychainService, account, token) }
		}
		if deleteToken == nil {
			deleteToken = deleteExternalToken
		}
	}
	allowed := func(w http.ResponseWriter, r *http.Request, method string) bool {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != method || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return false
		}
		return true
	}
	mux.HandleFunc("/api/external-mcp", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r, "GET") {
			return
		}
		root, err := externalRoot()
		if err != nil {
			http.Error(w, "Could not locate MCP settings.", 500)
			return
		}
		servers, err := listExternal(root)
		if err != nil {
			http.Error(w, "Could not read MCP settings.", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(servers)
	})
	mux.HandleFunc("/api/external-mcp/save", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r, "POST") {
			return
		}
		var input struct {
			Server     externalServer `json:"server"`
			Token      string         `json:"token"`
			ClearToken bool           `json:"clear_token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
			http.Error(w, "Invalid request.", 400)
			return
		}
		cfg := input.Server
		cfg.HasToken = false
		cfg.TokenAccount = ""
		cfg.Revision = 0
		if err := validateExternal(cfg); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if len(input.Token) > 8192 || strings.ContainsAny(input.Token, "\r\n") {
			http.Error(w, "Invalid bearer token.", 400)
			return
		}
		if input.Token != "" && cfg.Transport != "http" {
			http.Error(w, "Bearer tokens are only supported for HTTP servers.", 400)
			return
		}
		root, err := externalRoot()
		if err != nil {
			http.Error(w, "Could not locate MCP settings.", 500)
			return
		}
		externalSettingsMu.Lock()
		defer externalSettingsMu.Unlock()
		old, _ := readExternal(root, cfg.Name)
		createdAccount := ""
		if input.Token != "" {
			if saveToken == nil || deleteToken == nil {
				http.Error(w, "Saving bearer tokens requires macOS Keychain.", 400)
				return
			}
			random := make([]byte, 16)
			if _, err = rand.Read(random); err != nil {
				http.Error(w, "Could not create token entry.", 500)
				return
			}
			createdAccount = "mcp-" + hex.EncodeToString(random)
			if err = saveToken(createdAccount, input.Token); err != nil {
				http.Error(w, "Could not save token in Keychain.", 500)
				return
			}
			cfg.HasToken = true
			cfg.TokenAccount = createdAccount
		} else if !input.ClearToken && cfg.Transport == "http" && old.Transport == "http" && cfg.URL == old.URL && old.HasToken {
			cfg.HasToken = true
			cfg.TokenAccount = old.TokenAccount
		}
		if err = saveExternal(root, cfg); err != nil {
			if createdAccount != "" {
				_ = deleteToken(createdAccount)
			}
			http.Error(w, "Could not save MCP server.", 500)
			return
		}
		warning := ""
		if old.HasToken && old.TokenAccount != cfg.TokenAccount {
			if deleteToken == nil || deleteToken(old.TokenAccount) != nil {
				warning = "Server saved, but the previous Keychain entry could not be removed."
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"warning": warning})
	})
	mux.HandleFunc("/api/external-mcp/remove", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r, "POST") {
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
			http.Error(w, "Invalid request.", 400)
			return
		}
		root, err := externalRoot()
		if err != nil {
			http.Error(w, "Could not locate MCP settings.", 500)
			return
		}
		externalSettingsMu.Lock()
		defer externalSettingsMu.Unlock()
		old, err := readExternal(root, input.Name)
		if err != nil {
			http.Error(w, "Server not found.", 404)
			return
		}
		if err = removeExternal(root, input.Name); err != nil {
			http.Error(w, "Could not remove MCP server.", 500)
			return
		}
		warning := ""
		if old.HasToken {
			if deleteToken == nil || deleteToken(old.TokenAccount) != nil {
				warning = "Server removed, but its Keychain entry could not be removed."
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"warning": warning})
	})
}
