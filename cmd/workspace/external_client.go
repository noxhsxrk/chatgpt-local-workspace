package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type externalSession struct {
	fingerprint [32]byte
	session     *mcp.ClientSession
	token       string
}
type externalManager struct {
	mu            sync.Mutex
	sessions      map[string]*externalSession
	done          chan struct{}
	workspace     string
	once          sync.Once
	httpTransport http.RoundTripper
	loadToken     func(string) (string, error)
}

func newExternalManager(workspace string) *externalManager {
	m := &externalManager{sessions: map[string]*externalSession{}, done: make(chan struct{}), workspace: workspace}
	go m.watch()
	return m
}
func (m *externalManager) watch() {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-tick.C:
			m.mu.Lock()
			root, err := externalRoot()
			if err == nil {
				for name, s := range m.sessions {
					c, e := readExternal(root, name)
					if e != nil || externalFingerprint(c) != s.fingerprint {
						delete(m.sessions, name)
						_ = s.session.Close()
					}
				}
			}
			m.mu.Unlock()
		}
	}
}
func externalFingerprint(c externalServer) [32]byte {
	data, _ := json.Marshal(c)
	return sha256.Sum256(data)
}
func (m *externalManager) close() {
	m.once.Do(func() {
		close(m.done)
		m.mu.Lock()
		defer m.mu.Unlock()
		for name, s := range m.sessions {
			delete(m.sessions, name)
			_ = s.session.Close()
		}
	})
}
func externalEnv(allow []string) []string {
	names := []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "LANG"}
	names = append(names, allow...)
	seen := map[string]bool{}
	var env []string
	for _, name := range names {
		if seen[name] || name == "CONTROL_PLANE_API_KEY" || strings.HasPrefix(name, "WORKSPACE_") {
			continue
		}
		seen[name] = true
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+val)
		}
	}
	return env
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

// External stdio servers run in their own process group. Closing the session
// also asks children of the server to stop, rather than leaving them orphaned.
type groupedCommandTransport struct {
	inner *mcp.CommandTransport
	cmd   *exec.Cmd
}
type trackingTransport struct {
	inner mcp.Transport
	conn  mcp.Connection
}

func (t *trackingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err == nil {
		t.conn = c
	}
	return c, err
}

func (t *groupedCommandTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &groupedConnection{Connection: c, cmd: t.cmd}, nil
}

type groupedConnection struct {
	mcp.Connection
	cmd  *exec.Cmd
	once sync.Once
	err  error
}

func (c *groupedConnection) Close() error {
	c.once.Do(func() {
		if c.cmd.Process != nil {
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		}
		c.err = c.Connection.Close()
	})
	return c.err
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}
func (m *externalManager) connect(ctx context.Context, name string) (*externalSession, error) {
	root, err := externalRoot()
	if err != nil {
		return nil, err
	}
	cfg, err := readExternal(root, name)
	if err != nil {
		if old := m.sessions[name]; old != nil {
			delete(m.sessions, name)
			_ = old.session.Close()
		}
		return nil, errors.New("external MCP server is not configured")
	}
	if old := m.sessions[name]; old != nil {
		if old.fingerprint == externalFingerprint(cfg) {
			return old, nil
		}
		delete(m.sessions, name)
		_ = old.session.Close()
	}
	if len(m.sessions) >= 32 {
		return nil, errors.New("external MCP session limit reached")
	}
	var transport mcp.Transport
	var token string
	if cfg.Transport == "stdio" {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Dir = m.workspace
		cmd.Env = externalEnv(cfg.Env)
		cmd.Stderr = io.Discard
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		transport = &groupedCommandTransport{inner: &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, cmd: cmd}
	} else {
		base := http.RoundTripper(http.DefaultTransport)
		if m.httpTransport != nil {
			base = m.httpTransport
		}
		httpClient := &http.Client{Transport: base, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if cfg.HasToken {
			if m.loadToken != nil {
				token, err = m.loadToken(cfg.TokenAccount)
			} else {
				token, err = loadKeychainEntry(externalKeychainService, cfg.TokenAccount)
			}
			if err != nil {
				return nil, errors.New("could not access external server token in Keychain")
			}
			httpClient.Transport = &bearerTransport{base: base, token: token}
		}
		transport = &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient, MaxRetries: -1}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "chatgpt-local-workspace", Version: appVersion}, nil)
	tracked := &trackingTransport{inner: transport}
	session, err := client.Connect(ctx, tracked, nil)
	if err != nil {
		if tracked.conn != nil {
			_ = tracked.conn.Close()
		}
		msg := err.Error()
		if token != "" {
			msg = strings.ReplaceAll(msg, token, "[redacted]")
		}
		return nil, fmt.Errorf("connect to external MCP server: %s", secretPattern.ReplaceAllString(msg, "[redacted]"))
	}
	s := &externalSession{fingerprint: externalFingerprint(cfg), session: session, token: token}
	m.sessions[name] = s
	return s, nil
}
func (m *externalManager) listTools(name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, err := m.connect(ctx, name)
	if err != nil {
		return "", err
	}
	var tools []*mcp.Tool
	cursor := ""
	truncated := false
	for page := 0; page < 5; page++ {
		result, err := s.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			delete(m.sessions, name)
			_ = s.session.Close()
			msg := err.Error()
			if s.token != "" {
				msg = strings.ReplaceAll(msg, s.token, "[redacted]")
			}
			msg = secretPattern.ReplaceAllString(msg, "[redacted]")
			return "", fmt.Errorf("list tools: %s", msg)
		}
		tools = append(tools, result.Tools...)
		if len(tools) > 100 {
			tools = tools[:100]
			truncated = true
			break
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
		if page == 4 {
			truncated = true
		}
	}
	data, err := json.Marshal(map[string]any{"tools": tools, "truncated": truncated})
	if err != nil {
		return "", err
	}
	if len(data) > maxFileBytes {
		return "", errors.New("external tool list exceeds 256 KiB")
	}
	return string(data), nil
}
func (m *externalManager) callTool(name, tool string, args json.RawMessage) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tool == "" || len(tool) > 512 {
		return "", false, errors.New("tool name is required")
	}
	var object map[string]any
	if len(args) == 0 {
		object = map[string]any{}
	} else if err := json.Unmarshal(args, &object); err != nil || object == nil {
		return "", false, errors.New("arguments must be a JSON object")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := m.connect(ctx, name)
	if err != nil {
		return "", false, err
	}
	result, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: object})
	if err != nil {
		delete(m.sessions, name)
		_ = s.session.Close()
		msg := err.Error()
		if s.token != "" {
			msg = strings.ReplaceAll(msg, s.token, "[redacted]")
		}
		msg = secretPattern.ReplaceAllString(msg, "[redacted]")
		return "", false, fmt.Errorf("external tool failed; its side effects may have occurred: %s", msg)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", false, err
	}
	if len(data) > maxFileBytes {
		return "", false, errors.New("external tool result exceeds 256 KiB; its side effects may have occurred")
	}
	return string(data), result.IsError, nil
}
