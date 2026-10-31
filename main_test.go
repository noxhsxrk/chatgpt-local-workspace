package main

import (
	"os"
	"path/filepath"
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
