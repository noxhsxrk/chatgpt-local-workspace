package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeSkill(t *testing.T, base, name string) string {
	t.Helper()
	p := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(p, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: A useful skill\n---\n# Instructions\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "references", "notes.md"), []byte("reference text"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSkillInstallListReadAndRemove(t *testing.T) {
	base := t.TempDir()
	source := makeSkill(t, base, "example-skill")
	root := filepath.Join(t.TempDir(), "managed")
	skill, err := installSkill(root, source)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Name != "example-skill" {
		t.Fatalf("unexpected skill: %+v", skill)
	}
	skills, err := listSkills(root)
	if err != nil || len(skills) != 1 || skills[0].Description != "A useful skill" {
		t.Fatalf("list: %+v %v", skills, err)
	}
	data, err := readSkillFile(root, skill.Name, "references/notes.md")
	if err != nil || data != "reference text" {
		t.Fatalf("read: %q %v", data, err)
	}
	if _, err = readSkillFile(root, skill.Name, "../secret"); err == nil {
		t.Fatal("path traversal allowed")
	}
	if _, err = installSkill(root, source); err == nil {
		t.Fatal("duplicate installation allowed")
	}
	if err = removeSkill(root, skill.Name); err != nil {
		t.Fatal(err)
	}
	skills, err = listSkills(root)
	if err != nil || len(skills) != 0 {
		t.Fatalf("remove: %+v %v", skills, err)
	}
}
func TestSkillInstallRejectsSymlinksAndInvalidMetadata(t *testing.T) {
	base := t.TempDir()
	source := makeSkill(t, base, "example-skill")
	root := filepath.Join(t.TempDir(), "managed")
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := installSkill(root, source); err == nil {
		t.Fatal("symlink allowed")
	}
	if err := os.Remove(filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: wrong\ndescription: test\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installSkill(root, source); err == nil {
		t.Fatal("mismatched name accepted")
	}
}

func TestSkillRejectsOversizedPackageAndSymlinkRead(t *testing.T) {
	base := t.TempDir()
	source := makeSkill(t, base, "example-skill")
	root := filepath.Join(t.TempDir(), "managed")
	if err := os.WriteFile(filepath.Join(source, "large.bin"), make([]byte, maxSkillBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installSkill(root, source); err == nil {
		t.Fatal("oversized skill accepted")
	}
	if err := os.Remove(filepath.Join(source, "large.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := installSkill(root, source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(source, "SKILL.md"), filepath.Join(root, "example-skill", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSkillFile(root, "example-skill", "link"); err == nil {
		t.Fatal("symlink read accepted")
	}
}
func TestSkillToolsUseManagedLibrary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := skillRoot()
	if err != nil {
		t.Fatal(err)
	}
	source := makeSkill(t, t.TempDir(), "example-skill")
	if _, err = installSkill(root, source); err != nil {
		t.Fatal(err)
	}
	s := &server{root: t.TempDir()}
	list := s.call("skills_list", json.RawMessage(`{}`))
	if list.IsError || !strings.Contains(list.Content[0].Text, "example-skill") {
		t.Fatalf("list tool: %+v", list)
	}
	read := s.call("skills_read", json.RawMessage(`{"name":"example-skill"}`))
	if read.IsError || !strings.Contains(read.Content[0].Text, "Instructions") {
		t.Fatalf("read tool: %+v", read)
	}
}

func TestSkillsWebAPIRequiresTokenAndInstallsOnlyOnRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := makeSkill(t, t.TempDir(), "example-skill")
	m := &webManager{token: "secret"}
	payload, _ := json.Marshal(map[string]string{"path": source})
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/skills/install", bytes.NewReader(payload))
		r.Header.Set("X-Workspace-Token", token)
		w := httptest.NewRecorder()
		m.handler().ServeHTTP(w, r)
		return w
	}
	if w := call("wrong"); w.Code != http.StatusForbidden {
		t.Fatalf("unauthorized install: %d", w.Code)
	}
	if w := call("secret"); w.Code != http.StatusOK {
		t.Fatalf("install: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/skills", nil)
	r.Header.Set("X-Workspace-Token", "secret")
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "example-skill") {
		t.Fatalf("missing installed skill: %s", w.Body)
	}
}

func TestMCPStdioCanDiscoverAndReadManagedSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := skillRoot()
	if err != nil {
		t.Fatal(err)
	}
	source := makeSkill(t, t.TempDir(), "example-skill")
	if _, err = installSkill(root, source); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPLogProcessHelper$")
	cmd.Env = append(os.Environ(), "WORKSPACE_TEST_HELPER=1", "WORKSPACE_ROOT="+t.TempDir(), "WORKSPACE_AUDIT_LOG=")
	cmd.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"skills_list\",\"arguments\":{}}}\n{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"skills_read\",\"arguments\":{\"name\":\"example-skill\"}}}\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("skills_list")) || !bytes.Contains(out, []byte("skills_read")) || !bytes.Contains(out, []byte("Instructions")) {
		t.Fatalf("missing MCP response: %s", out)
	}
}
