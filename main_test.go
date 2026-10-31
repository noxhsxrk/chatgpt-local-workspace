package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceReadWriteAndSandbox(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{root: root}
	result := s.call("write_file", []byte(`{"path":"src/hello.txt","content":"Hello MCP"}`))
	if result.IsError {
		t.Fatalf("write: %+v", result)
	}
	result = s.call("read_file", []byte(`{"path":"src/hello.txt"}`))
	if result.IsError || result.Content[0].Text != "Hello MCP" {
		t.Fatalf("read: %+v", result)
	}
	result = s.call("read_file", []byte(`{"path":"../secret"}`))
	if !result.IsError {
		t.Fatal("traversal was allowed")
	}
	result = s.call("write_file", []byte(`{"path":".env","content":"secret"}`))
	if !result.IsError {
		t.Fatal(".env was allowed")
	}
	result = s.call("write_file", []byte(`{"path":".ENV.LOCAL","content":"secret"}`))
	if !result.IsError {
		t.Fatal("uppercase .ENV.LOCAL was allowed")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	result = s.call("write_file", []byte(`{"path":"escape/forbidden.txt","content":"bad"}`))
	if !result.IsError {
		t.Fatal("symlink escape was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped workspace")
	}
}

func TestSearchTextIsBoundedToWorkspaceAndSkipsSecrets(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "src", "main.txt"), []byte("first\nneedle here\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".env"), []byte("needle secret"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err = os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("needle outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	s := &server{root: root}
	result := s.call("search_text", []byte(`{"query":"needle"}`))
	if result.IsError || !strings.Contains(result.Content[0].Text, `"src/main.txt":2: needle here`) || strings.Contains(result.Content[0].Text, "secret") || strings.Contains(result.Content[0].Text, "outside") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result = s.call("search_text", []byte(`{"path":"../","query":"needle"}`)); !result.IsError {
		t.Fatal("search escaped workspace")
	}
	if result = s.call("search_text", []byte(`{"query":""}`)); !result.IsError {
		t.Fatal("empty query accepted")
	}
}
