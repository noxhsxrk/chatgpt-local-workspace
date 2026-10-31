package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/index.html assets/*.png
var webAssets embed.FS

type webManager struct {
	mu                                sync.Mutex
	cmd                               *exec.Cmd
	state, message, workspace, tunnel string
	generation                        uint64
	token                             string
	binary                            string
	key                               string
	saveKey                           func(string) error
	saveSettings                      func(string, string) error
	keyStored                         bool
	auditPath                         string
	logEnabled                        bool
	authenticate                      func() error
	externalSaveToken                 func(string, string) error
	externalDeleteToken               func(string) error
}
type webSettings struct {
	Tunnel    string `json:"tunnel"`
	Key       string `json:"key"`
	Workspace string `json:"workspace"`
	Log       bool   `json:"log"`
}

var tunnelPattern = regexp.MustCompile(`^tunnel_[A-Za-z0-9_-]+$`)
var secretPattern = regexp.MustCompile(`(?i)(sk-[a-zA-Z0-9_-]+|bearer\s+[^\s"']+)`)

type limitedLog struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (l *limitedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	available := 16384 - l.buf.Len()
	if available < len(p) {
		l.truncated = true
		if available < 0 {
			available = 0
		}
		p = p[:available]
	}
	_, _ = l.buf.Write(p)
	return n, nil
}
func (l *limitedLog) safeMessage(key string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.buf.String()
	if l.truncated {
		if i := strings.LastIndex(s, "\n"); i >= 0 {
			s = s[:i]
		} else {
			s = ""
		}
	}
	s = strings.ReplaceAll(s, key, "[redacted]")
	s = secretPattern.ReplaceAllString(s, "[redacted]")
	s = strings.TrimSpace(s)
	if len(s) > 1500 {
		s = s[len(s)-1500:]
	}
	return s
}

func (m *webManager) status(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	home, _ := os.UserHomeDir()
	json.NewEncoder(w).Encode(map[string]any{"state": m.state, "message": m.message, "workspace": m.workspace, "tunnel": m.tunnel, "default_workspace": home, "has_key": m.key != "", "log_enabled": m.logEnabled})
}
func (m *webManager) start(w http.ResponseWriter, r *http.Request) {
	var s webSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&s); err != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	m.mu.Lock()
	if s.Key == "" {
		s.Key = m.key
	}
	m.mu.Unlock()
	if !tunnelPattern.MatchString(s.Tunnel) || strings.TrimSpace(s.Key) == "" || !filepath.IsAbs(s.Workspace) {
		http.Error(w, "Enter a valid tunnel ID, API key and absolute workspace path.", 400)
		return
	}
	root, err := filepath.EvalSymlinks(s.Workspace)
	if err != nil {
		http.Error(w, "Workspace directory does not exist.", 400)
		return
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		http.Error(w, "Workspace must be a directory.", 400)
		return
	}
	client, err := exec.LookPath("tunnel-client")
	if err != nil {
		http.Error(w, "tunnel-client not found. Install it with brew install openai/tools/tunnel-client.", 400)
		return
	}
	m.mu.Lock()
	if m.state == "starting" || m.state == "running" {
		m.mu.Unlock()
		http.Error(w, "Stop the current tunnel first.", 409)
		return
	}
	var auditPath string
	if s.Log {
		f, err := os.CreateTemp("", "workspace-mcp-log-*.jsonl")
		if err != nil {
			m.mu.Unlock()
			http.Error(w, "Could not create request log.", 500)
			return
		}
		auditPath = f.Name()
		if err := f.Close(); err != nil {
			_ = os.Remove(auditPath)
			m.mu.Unlock()
			http.Error(w, "Could not create request log.", 500)
			return
		}
	}
	keepLog := false
	defer func() {
		if !keepLog && auditPath != "" {
			_ = os.Remove(auditPath)
		}
	}()
	if m.saveKey != nil {
		if s.Key != m.key || !m.keyStored {
			if err := m.saveKey(s.Key); err != nil {
				m.mu.Unlock()
				http.Error(w, "Could not save the API key in macOS Keychain. Check Keychain access and try again.", 500)
				return
			}
			m.keyStored = true
		}
	}
	if m.saveSettings != nil {
		if err := m.saveSettings(s.Tunnel, root); err != nil {
			m.mu.Unlock()
			http.Error(w, "Could not save connection settings.", 500)
			return
		}
	}
	m.key = s.Key
	if m.auditPath != "" {
		_ = os.Remove(m.auditPath)
	}
	m.auditPath, m.logEnabled = auditPath, s.Log
	keepLog = true
	m.generation++
	gen := m.generation
	m.state = "starting"
	m.message = "Preparing tunnel configuration"
	m.workspace = root
	m.tunnel = s.Tunnel
	m.mu.Unlock()
	go func() {
		profile := fmt.Sprintf("workspace-web-%d", time.Now().UnixNano())
		env := append(os.Environ(), "CONTROL_PLANE_API_KEY="+s.Key, "WORKSPACE_ROOT="+root, "WORKSPACE_AUDIT_LOG="+auditPath)
		init := exec.Command(client, "init", "--sample", "sample_mcp_stdio_local", "--profile", profile, "--tunnel-id", s.Tunnel, "--mcp-command", m.binary)
		init.Env = env
		var initLog limitedLog
		init.Stdout, init.Stderr = &initLog, &initLog
		if err := init.Run(); err != nil {
			m.mu.Lock()
			if m.generation == gen {
				m.state = "error"
				m.message = "Could not create the tunnel profile. Check your tunnel-client installation."
				if detail := initLog.safeMessage(s.Key); detail != "" {
					m.message += "\n" + detail
				}
			}
			m.mu.Unlock()
			return
		}
		m.mu.Lock()
		if m.generation != gen {
			m.mu.Unlock()
			return
		}
		cmd := exec.Command(client, "run", "--profile", profile, "--health.listen-addr", "127.0.0.1:0")
		cmd.Env = env
		var runLog limitedLog
		cmd.Stdout, cmd.Stderr = &runLog, &runLog
		if err := cmd.Start(); err != nil {
			m.state = "error"
			m.message = "Could not start tunnel-client."
			m.mu.Unlock()
			return
		}
		m.cmd = cmd
		m.state = "running"
		m.message = "Tunnel client is running. Check connectivity in ChatGPT."
		m.mu.Unlock()
		err := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.generation == gen {
			m.cmd = nil
			if err != nil {
				m.state = "error"
				m.message = "Tunnel client stopped. Check the ID, API key and network connection."
				if detail := runLog.safeMessage(s.Key); detail != "" {
					m.message += "\n" + detail
				}
			} else {
				m.state = "stopped"
				m.message = "Tunnel stopped."
			}
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}
func (m *webManager) stop(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		m.cmd = nil
	}
	m.state = "stopped"
	m.message = "Tunnel stopped."
	w.WriteHeader(http.StatusNoContent)
}
func (m *webManager) handler() http.Handler {
	mux := http.NewServeMux()
	registerExternalRoutes(mux, m)
	mux.Handle("/assets/", http.FileServer(http.FS(webAssets)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page, _ := webAssets.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		content := strings.ReplaceAll(string(page), "__CSRF_TOKEN__", m.token)
		fmt.Fprint(w, strings.ReplaceAll(content, "__APP_VERSION__", appVersion))
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		m.status(w, r)
	})
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", 403)
			return
		}
		m.start(w, r)
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", 403)
			return
		}
		m.stop(w, r)
	})
	mux.HandleFunc("/api/reveal-key", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		m.mu.Lock()
		hasKey, authenticate := m.key != "", m.authenticate
		m.mu.Unlock()
		if !hasKey || authenticate == nil {
			http.Error(w, "No saved key or device authentication is unavailable.", http.StatusBadRequest)
			return
		}
		if err := authenticate(); err != nil {
			http.Error(w, "Device authentication was cancelled or failed.", http.StatusForbidden)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"key": m.key})
	})
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		m.mu.Lock()
		path := m.auditPath
		m.mu.Unlock()
		if path == "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "Could not read request log.", 500)
			return
		}
		if len(data) > 256<<10 {
			data = data[len(data)-(256<<10):]
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/api/usage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		m.mu.Lock()
		path := m.auditPath
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if path == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false})
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "Could not read request log.", 500)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.Error(w, "Could not read request log.", 500)
			return
		}
		u, err := estimateMCPUsage(f)
		if err != nil {
			http.Error(w, "Could not estimate request log.", 500)
			return
		}
		if info.Size() >= 2<<20 {
			u.Incomplete = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "estimate": u})
	})
	mux.HandleFunc("/api/skills", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		root, err := skillRoot()
		if err != nil {
			http.Error(w, "Could not locate skill library.", 500)
			return
		}
		skills, err := listSkills(root)
		if err != nil {
			http.Error(w, "Could not read skill library.", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(skills)
	})
	mux.HandleFunc("/api/skills/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var input struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
			http.Error(w, "Invalid request.", 400)
			return
		}
		root, err := skillRoot()
		if err != nil {
			http.Error(w, "Could not locate skill library.", 500)
			return
		}
		skill, err := installSkill(root, input.Path)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(skill)
	})
	mux.HandleFunc("/api/skills/remove", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
			http.Error(w, "Invalid request.", 400)
			return
		}
		root, err := skillRoot()
		if err != nil {
			http.Error(w, "Could not locate skill library.", 500)
			return
		}
		if err := removeSkill(root, input.Name); err != nil {
			http.Error(w, "Could not remove skill.", 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/agents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		root, err := agentRoot()
		if err != nil {
			http.Error(w, "Could not locate agent profiles.", 500)
			return
		}
		profiles, err := listAgents(root)
		if err != nil {
			http.Error(w, "Could not read agent profiles.", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(profiles)
	})
	mux.HandleFunc("/api/agents/get", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		root, err := agentRoot()
		if err != nil {
			http.Error(w, "Could not locate agent profiles.", 500)
			return
		}
		profile, err := readAgent(root, r.URL.Query().Get("name"))
		if err != nil {
			http.Error(w, "Agent profile not found.", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(profile)
	})
	mux.HandleFunc("/api/agents/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var profile agentProfile
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 40<<10)).Decode(&profile); err != nil {
			http.Error(w, "Invalid request.", 400)
			return
		}
		root, err := agentRoot()
		if err != nil {
			http.Error(w, "Could not locate agent profiles.", 500)
			return
		}
		if err := saveAgent(root, profile); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/agents/remove", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Workspace-Token") != m.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
			http.Error(w, "Invalid request.", 400)
			return
		}
		root, err := agentRoot()
		if err != nil {
			http.Error(w, "Could not locate agent profiles.", 500)
			return
		}
		if err := removeAgent(root, input.Name); err != nil {
			http.Error(w, "Could not remove agent profile.", 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || host != "127.0.0.1" {
			http.Error(w, "Forbidden", 403)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}
func runWeb() {
	binary, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		log.Fatal(err)
	}
	hub := newTunnelHub(hex.EncodeToString(b), binary)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		hub.stopAll()
		os.Exit(0)
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	url := fmt.Sprintf("http://%s", listener.Addr())
	fmt.Printf("Open %s in your browser. Press Ctrl+C to stop.\n", url)
	if runtime.GOOS == "darwin" {
		if err := exec.Command("open", url).Start(); err != nil {
			log.Printf("Could not open browser automatically: %v", err)
		}
	}
	log.Fatal(http.Serve(listener, hub.handler()))
}
