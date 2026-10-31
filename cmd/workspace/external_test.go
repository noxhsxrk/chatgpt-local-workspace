package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExternalMCPHelper(t *testing.T) {
	if os.Getenv("MCP_TEST_HELPER") != "1" {
		return
	}
	count := 0
	s := mcp.NewServer(&mcp.Implementation{Name: "test-external", Version: "1"}, nil)
	type input struct {
		Text string `json:"text"`
	}
	type output struct {
		Count  int    `json:"count"`
		Text   string `json:"text"`
		Leaked bool   `json:"leaked"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo with a session counter"}, func(_ context.Context, _ *mcp.CallToolRequest, a input) (*mcp.CallToolResult, output, error) {
		count++
		return nil, output{Count: count, Text: a.Text, Leaked: os.Getenv("CONTROL_PLANE_API_KEY") != ""}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func TestExternalMCPStdioSessionAndEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MCP_TEST_HELPER", "1")
	t.Setenv("CONTROL_PLANE_API_KEY", "do-not-leak")
	root, err := externalRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := externalServer{Name: "example", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestExternalMCPHelper$"}, Env: []string{"MCP_TEST_HELPER"}}
	if err = saveExternal(root, cfg); err != nil {
		t.Fatal(err)
	}
	m := newExternalManager(t.TempDir())
	defer m.close()
	tools, err := m.listTools("example")
	if err != nil || !strings.Contains(tools, "echo") {
		t.Fatalf("tools: %s %v", tools, err)
	}
	for i, want := range []string{`"count":1`, `"count":2`} {
		out, isError, err := m.callTool("example", "echo", json.RawMessage(`{"text":"hello"}`))
		if err != nil || isError || !strings.Contains(out, want) || !strings.Contains(out, `"leaked":false`) {
			t.Fatalf("call %d: %s %v %v", i, out, isError, err)
		}
	}
	if err = removeExternal(root, "example"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.callTool("example", "echo", nil); err == nil {
		t.Fatal("removed MCP server was callable")
	}
}
func TestExternalConfigValidation(t *testing.T) {
	cases := []externalServer{
		{Name: "../escape", Transport: "stdio", Command: "echo"},
		{Name: "example", Transport: "stdio", Command: "echo", Env: []string{"CONTROL_PLANE_API_KEY"}},
		{Name: "example", Transport: "http", URL: "http://example.com/mcp"},
		{Name: "example", Transport: "http", URL: "https://user:secret@example.com/mcp"},
		{Name: "example", Transport: "http", URL: "https://example.com/mcp", HasToken: true, TokenAccount: "bad"},
	}
	for _, c := range cases {
		if err := validateExternal(c); err == nil {
			t.Fatalf("accepted invalid config: %+v", c)
		}
	}
	for _, u := range []string{"http://127.0.0.1:8080/mcp", "http://localhost:3000/mcp", "https://example.com/mcp"} {
		if err := validateExternal(externalServer{Name: "example", Transport: "http", URL: u}); err != nil {
			t.Fatalf("valid URL %s: %v", u, err)
		}
	}
}
func TestExternalConfigStoresNoBearerValue(t *testing.T) {
	root := filepath.Join(t.TempDir(), "servers")
	cfg := externalServer{Name: "example", Transport: "http", URL: "https://example.com/mcp", HasToken: true, TokenAccount: "mcp-0123456789abcdef0123456789abcdef"}
	if err := saveExternal(root, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Bearer ") {
		t.Fatal("token leaked to config")
	}
	loaded, err := readExternal(root, "example")
	if err != nil || !loaded.HasToken {
		t.Fatalf("config: %+v %v", loaded, err)
	}
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
func TestExternalMCPHTTPUsesConfiguredBearer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := externalRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := externalServer{Name: "remote", Transport: "http", URL: "https://example.test/mcp", HasToken: true, TokenAccount: "mcp-0123456789abcdef0123456789abcdef"}
	if err = saveExternal(root, cfg); err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test-http", Version: "1"}, nil)
	type input struct {
		Text string `json:"text"`
	}
	type output struct {
		Text string `json:"text"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo"}, func(_ context.Context, _ *mcp.CallToolRequest, a input) (*mcp.CallToolResult, output, error) {
		return nil, output{Text: a.Text}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	authHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler.ServeHTTP(w, r)
	})
	manager := newExternalManager(t.TempDir())
	manager.httpTransport = handlerTransport{handler: authHandler}
	manager.loadToken = func(string) (string, error) { return "test-secret", nil }
	defer manager.close()
	tools, err := manager.listTools("remote")
	if err != nil || !strings.Contains(tools, "echo") {
		t.Fatalf("HTTP tools: %s %v", tools, err)
	}
	out, isError, err := manager.callTool("remote", "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil || isError || !strings.Contains(out, "hello") {
		t.Fatalf("HTTP call: %s %v %v", out, isError, err)
	}
}

func TestExternalMCPToolsAreAvailableThroughLocalServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MCP_TEST_HELPER", "1")
	root, err := externalRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := externalServer{Name: "example", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestExternalMCPHelper$"}, Env: []string{"MCP_TEST_HELPER"}}
	if err = saveExternal(root, cfg); err != nil {
		t.Fatal(err)
	}
	s := &server{root: t.TempDir()}
	defer func() {
		if s.external != nil {
			s.external.close()
		}
	}()
	list := s.call("mcp_servers_list", json.RawMessage(`{}`))
	if list.IsError || !strings.Contains(list.Content[0].Text, "example") {
		t.Fatalf("server list: %+v", list)
	}
	tools := s.call("mcp_server_tools", json.RawMessage(`{"server":"example"}`))
	if tools.IsError || !strings.Contains(tools.Content[0].Text, "echo") {
		t.Fatalf("tool list: %+v", tools)
	}
	result := s.call("mcp_server_call", json.RawMessage(`{"server":"example","tool":"echo","arguments":{"text":"hello"}}`))
	if result.IsError || !strings.Contains(result.Content[0].Text, "hello") {
		t.Fatalf("call: %+v", result)
	}
}
func TestExternalMCPWebConfigurationRequiresAuthorization(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := &webManager{token: "secret"}
	payload := `{"server":{"name":"example","transport":"http","url":"https://example.test/mcp"}}`
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/external-mcp/save", strings.NewReader(payload))
		r.Header.Set("X-Workspace-Token", token)
		w := httptest.NewRecorder()
		m.handler().ServeHTTP(w, r)
		return w
	}
	if w := request("bad"); w.Code != http.StatusForbidden {
		t.Fatalf("unauthorized: %d", w.Code)
	}
	if w := request("secret"); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/external-mcp", nil)
	r.Header.Set("X-Workspace-Token", "secret")
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "example") {
		t.Fatalf("list: %s", w.Body)
	}
}

func TestExternalBearerSecretIsKeptOutOfConfigAndNotReusedForDifferentURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := map[string]string{}
	m := &webManager{token: "csrf", externalSaveToken: func(account, token string) error { store[account] = token; return nil }, externalDeleteToken: func(account string) error { delete(store, account); return nil }}
	save := func(payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/external-mcp/save", strings.NewReader(payload))
		r.Header.Set("X-Workspace-Token", "csrf")
		w := httptest.NewRecorder()
		m.handler().ServeHTTP(w, r)
		return w
	}
	if w := save(`{"server":{"name":"remote","transport":"http","url":"https://first.test/mcp"},"token":"private-bearer"}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "private-bearer") {
		t.Fatalf("save failed or leaked token: %d %s", w.Code, w.Body)
	}
	root, _ := externalRoot()
	data, err := os.ReadFile(filepath.Join(root, "remote.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-bearer") {
		t.Fatal("bearer saved as plaintext in config")
	}
	cfg, err := readExternal(root, "remote")
	if err != nil || !cfg.HasToken || store[cfg.TokenAccount] != "private-bearer" {
		t.Fatal("token not stored separately")
	}
	if w := save(`{"server":{"name":"remote","transport":"http","url":"https://second.test/mcp"}}`); w.Code != http.StatusOK {
		t.Fatalf("change url: %d %s", w.Code, w.Body)
	}
	cfg, err = readExternal(root, "remote")
	if err != nil || cfg.HasToken || len(store) != 0 {
		t.Fatal("token was retained for a different endpoint")
	}
}

func TestExternalMCPThroughWorkspaceStdio(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MCP_TEST_HELPER", "1")
	root, err := externalRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := externalServer{Name: "example", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestExternalMCPHelper$"}, Env: []string{"MCP_TEST_HELPER"}}
	if err = saveExternal(root, cfg); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPLogProcessHelper$")
	cmd.Env = append(os.Environ(), "WORKSPACE_TEST_HELPER=1", "WORKSPACE_ROOT="+t.TempDir(), "WORKSPACE_AUDIT_LOG=")
	cmd.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"mcp_server_tools\",\"arguments\":{\"server\":\"example\"}}}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"mcp_server_call\",\"arguments\":{\"server\":\"example\",\"tool\":\"echo\",\"arguments\":{\"text\":\"bridge-test\"}}}}\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("bridge-test")) || bytes.Contains(out, []byte(`"isError":true`)) {
		t.Fatalf("bridge failed: %s", out)
	}
}
