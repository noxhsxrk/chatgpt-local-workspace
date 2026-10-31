// A small educational MCP stdio server for a single local workspace.
// It implements the MCP initialization/tool subset for the 2025-06-18 protocol.
// For production, use the official github.com/modelcontextprotocol/go-sdk.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const maxMessageBytes = 2 << 20
const maxFileBytes = 256 << 10

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type server struct{ root string }

var blockedNames = map[string]bool{
	".git": true, ".env": true, ".ssh": true, ".aws": true,
	".npmrc": true, ".pypirc": true, ".netrc": true,
	"node_modules": true, ".venv": true,
}

func (s *server) resolve(relative string) (string, error) {
	if relative == "" {
		relative = "."
	}
	if filepath.IsAbs(relative) {
		return "", errors.New("absolute paths are not allowed")
	}
	normalized := filepath.Clean(relative)
	for _, part := range strings.Split(filepath.ToSlash(normalized), "/") {
		if blockedNames[part] || strings.HasPrefix(part, ".env.") {
			return "", errors.New("access to sensitive or excluded path denied")
		}
	}
	candidate := filepath.Join(s.root, normalized)
	if err := ensureInside(s.root, candidate); err != nil {
		return "", err
	}

	// Check any existing ancestor, including symlinks, against the authorized root.
	// A production service should additionally guard against filesystem races.
	for ancestor := candidate; ; ancestor = filepath.Dir(ancestor) {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			if err := ensureInside(s.root, resolved); err != nil {
				return "", err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if ancestor == s.root {
			break
		}
	}
	return candidate, nil
}

func ensureInside(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path is outside WORKSPACE_ROOT")
	}
	return nil
}

func toolText(text string) toolResult {
	return toolResult{Content: []mcpContent{{Type: "text", Text: text}}}
}
func toolErr(err error) toolResult {
	return toolResult{Content: []mcpContent{{Type: "text", Text: err.Error()}}, IsError: true}
}

func (s *server) call(name string, args json.RawMessage) toolResult {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return toolErr(err)
	}
	if a.Path == "" && name != "list_dir" {
		return toolErr(errors.New("path is required"))
	}
	target, err := s.resolve(a.Path)
	if err != nil {
		return toolErr(err)
	}
	switch name {
	case "list_dir":
		entries, err := os.ReadDir(target)
		if err != nil {
			return toolErr(err)
		}
		var lines []string
		for _, entry := range entries {
			name := entry.Name()
			if blockedNames[name] || strings.HasPrefix(name, ".env.") {
				continue
			}
			suffix := ""
			if entry.IsDir() {
				suffix = "/"
			}
			if entry.Type()&os.ModeSymlink != 0 {
				suffix = " (symlink)"
			}
			lines = append(lines, name+suffix)
			if len(lines) >= 200 {
				lines = append(lines, "... truncated ...")
				break
			}
		}
		return toolText(strings.Join(lines, "\n"))
	case "read_file":
		info, err := os.Stat(target)
		if err != nil {
			return toolErr(err)
		}
		if !info.Mode().IsRegular() {
			return toolErr(errors.New("not a regular file"))
		}
		if info.Size() > maxFileBytes {
			return toolErr(errors.New("file too large (256 KiB limit)"))
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return toolErr(err)
		}
		return toolText(string(data))
	case "write_file":
		if len(a.Content) > maxFileBytes {
			return toolErr(errors.New("content too large (256 KiB limit)"))
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return toolErr(err)
		}
		// Verify again after directory creation.
		target, err = s.resolve(a.Path)
		if err != nil {
			return toolErr(err)
		}
		if err := os.WriteFile(target, []byte(a.Content), 0644); err != nil {
			return toolErr(err)
		}
		return toolText("Wrote " + a.Path)
	default:
		return toolErr(fmt.Errorf("unknown tool: %s", name))
	}
}

func (s *server) handle(req jsonrpcRequest) any {
	switch req.Method {
	case "initialize":
		// MCP clients can negotiate down to a supported protocol version.
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "own-mcp-workspace", "version": "0.1.0"},
		}
	case "ping":
		return map[string]any{}
	case "tools/list":
		return map[string]any{"tools": []any{
			map[string]any{"name": "list_dir", "description": "List up to 200 files or folders relative to the authorized workspace root.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Relative path, default ."}}}},
			map[string]any{"name": "read_file", "description": "Read a small text file relative to the workspace root.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}},
			map[string]any{"name": "write_file", "description": "Create or replace a text file under the workspace root. This changes data on disk.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}}},
		}}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return toolErr(err)
		}
		return s.call(p.Name, p.Arguments)
	default:
		return nil
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--web" {
		runWeb()
		return
	}
	root := os.Getenv("WORKSPACE_ROOT")
	if root == "" {
		log.Fatal("WORKSPACE_ROOT must be set to an existing local directory")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		log.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		log.Fatal(err)
	}
	st, err := os.Stat(canonical)
	if err != nil || !st.IsDir() {
		log.Fatal("WORKSPACE_ROOT is not a directory")
	}
	s := &server{root: canonical}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), maxMessageBytes)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req jsonrpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			log.Printf("invalid JSON-RPC: %v", err)
			continue
		}
		if len(req.ID) == 0 {
			continue
		} // MCP notifications have no response.
		result := s.handle(req)
		response := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID}
		if result == nil {
			response.Error = &jsonrpcError{Code: -32601, Message: "Method not found"}
		} else {
			response.Result = result
		}
		if err := out.Encode(response); err != nil {
			log.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}
