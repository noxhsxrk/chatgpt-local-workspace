package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentProfilesAndMCPTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := agentRoot()
	if err != nil {
		t.Fatal(err)
	}
	profile := agentProfile{Name: "reviewer", Description: "Review code", Instructions: "Check correctness and risks."}
	if err = saveAgent(root, profile); err != nil {
		t.Fatal(err)
	}
	if err = saveAgent(root, agentProfile{Name: "../escape", Description: "x", Instructions: "x"}); err == nil {
		t.Fatal("accepted invalid name")
	}
	list, err := listAgents(root)
	if err != nil || len(list) != 1 || list[0].Name != "reviewer" {
		t.Fatalf("list: %+v %v", list, err)
	}
	s := &server{root: t.TempDir()}
	result := s.call("agents_read", json.RawMessage(`{"name":"reviewer"}`))
	if result.IsError || !strings.Contains(result.Content[0].Text, profile.Instructions) {
		t.Fatalf("tool result: %+v", result)
	}
	if err = removeAgent(root, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err = readAgent(root, "reviewer"); err == nil {
		t.Fatal("deleted agent still readable")
	}
}
func TestAgentSaveRequiresToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := &webManager{token: "secret"}
	payload, _ := json.Marshal(agentProfile{Name: "reviewer", Description: "Review", Instructions: "Review carefully"})
	r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/agents/save", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unauthorized save: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:1234/api/agents/save", bytes.NewReader(payload))
	r.Header.Set("X-Workspace-Token", "secret")
	w = httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
}
