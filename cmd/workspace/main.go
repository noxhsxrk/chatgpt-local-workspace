// A small educational MCP stdio server for a single local workspace.
// It implements the MCP initialization/tool subset for the 2025-06-18 protocol.
// For production, use the official github.com/modelcontextprotocol/go-sdk.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxMessageBytes = 2 << 20
const maxFileBytes = 256 << 10
const appVersion = "0.2.8"

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

type server struct {
	root     string
	external *externalManager
}

var blockedNames = map[string]bool{
	".git": true, ".env": true, ".ssh": true, ".aws": true,
	".npmrc": true, ".pypirc": true, ".netrc": true,
	"node_modules": true, ".venv": true,
}

func isBlockedName(name string) bool {
	name = strings.ToLower(name)
	return blockedNames[name] || strings.HasPrefix(name, ".env.")
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
		if isBlockedName(part) {
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

func (s *server) searchText(target, query string) toolResult {
	const maxVisited = 5000
	const maxFiles = 500
	const maxMatches = 100
	visited, files := 0, 0
	var matches []string
	truncated, inaccessible := false, false
	stop := errors.New("search limit reached")
	err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == target {
				return walkErr
			}
			inaccessible = true
			return nil
		}
		visited++
		if visited > maxVisited {
			truncated = true
			return stop
		}
		if path != s.root {
			name := entry.Name()
			if isBlockedName(name) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			inaccessible = true
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			return nil
		}
		files++
		if files > maxFiles {
			truncated = true
			return stop
		}
		file, err := os.Open(path)
		if err != nil {
			inaccessible = true
			return nil
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
		_ = file.Close()
		if readErr != nil {
			inaccessible = true
			return nil
		}
		if len(data) > maxFileBytes || !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, query) {
				continue
			}
			if len(matches) >= maxMatches {
				truncated = true
				return stop
			}
			runes := []rune(strings.TrimSuffix(line, "\r"))
			if len(runes) > 512 {
				runes = append(runes[:512], '…')
			}
			matches = append(matches, fmt.Sprintf("%q:%d: %s", filepath.ToSlash(rel), i+1, string(runes)))
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return toolErr(err)
	}
	if truncated {
		matches = append(matches, "Search stopped at a safety limit; results may be incomplete.")
	}
	if inaccessible {
		matches = append(matches, "Some files or directories could not be read; results may be incomplete.")
	}
	if len(matches) == 0 {
		return toolText("No matches found.")
	}
	return toolText(strings.Join(matches, "\n"))
}

func (s *server) call(name string, args json.RawMessage) toolResult {
	var a struct {
		Path      string          `json:"path"`
		Content   string          `json:"content"`
		Query     string          `json:"query"`
		Name      string          `json:"name"`
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return toolErr(err)
	}
	if name == "skills_list" || name == "skills_read" {
		root, err := skillRoot()
		if err != nil {
			return toolErr(err)
		}
		if name == "skills_list" {
			skills, err := listSkills(root)
			if err != nil {
				return toolErr(err)
			}
			data, err := json.Marshal(skills)
			if err != nil {
				return toolErr(err)
			}
			return toolText(string(data))
		}
		content, err := readSkillFile(root, a.Name, a.Path)
		if err != nil {
			return toolErr(err)
		}
		return toolText(content)
	}
	if name == "agents_list" || name == "agents_read" {
		root, err := agentRoot()
		if err != nil {
			return toolErr(err)
		}
		if name == "agents_list" {
			profiles, err := listAgents(root)
			if err != nil {
				return toolErr(err)
			}
			data, err := json.Marshal(profiles)
			if err != nil {
				return toolErr(err)
			}
			return toolText(string(data))
		}
		profile, err := readAgent(root, a.Name)
		if err != nil {
			return toolErr(err)
		}
		data, err := json.Marshal(profile)
		if err != nil {
			return toolErr(err)
		}
		return toolText(string(data))
	}
	if name == "mcp_servers_list" || name == "mcp_server_tools" || name == "mcp_server_call" {
		root, err := externalRoot()
		if err != nil {
			return toolErr(err)
		}
		if name == "mcp_servers_list" {
			servers, err := listExternal(root)
			if err != nil {
				return toolErr(err)
			}
			type summary struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Transport   string `json:"transport"`
			}
			items := make([]summary, 0, len(servers))
			for _, c := range servers {
				items = append(items, summary{c.Name, c.Description, c.Transport})
			}
			data, err := json.Marshal(items)
			if err != nil {
				return toolErr(err)
			}
			return toolText(string(data))
		}
		if !validSkillName(a.Server) {
			return toolErr(errors.New("invalid external MCP server name"))
		}
		if s.external == nil {
			s.external = newExternalManager(s.root)
		}
		if name == "mcp_server_tools" {
			result, err := s.external.listTools(a.Server)
			if err != nil {
				return toolErr(err)
			}
			return toolText(result)
		}
		result, isError, err := s.external.callTool(a.Server, a.Tool, a.Arguments)
		if err != nil {
			return toolErr(err)
		}
		return toolResult{Content: []mcpContent{{Type: "text", Text: result}}, IsError: isError}
	}
	if a.Path == "" && name != "list_dir" && name != "search_text" {
		return toolErr(errors.New("path is required"))
	}
	target, err := s.resolve(a.Path)
	if err != nil {
		return toolErr(err)
	}
	switch name {
	case "search_text":
		if a.Query == "" || len(a.Query) > 256 || strings.ContainsAny(a.Query, "\r\n") {
			return toolErr(errors.New("query must be a single line of 1 to 256 bytes"))
		}
		return s.searchText(target, a.Query)
	case "list_dir":
		entries, err := os.ReadDir(target)
		if err != nil {
			return toolErr(err)
		}
		var lines []string
		for _, entry := range entries {
			name := entry.Name()
			if isBlockedName(name) {
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
			"serverInfo":      map[string]any{"name": "own-mcp-workspace", "version": appVersion},
		}
	case "ping":
		return map[string]any{}
	case "tools/list":
		return map[string]any{"tools": []any{
			map[string]any{"name": "list_dir", "description": "List up to 200 files or folders relative to the authorized workspace root.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Relative path, default ."}}}},
			map[string]any{"name": "search_text", "description": "Search for a literal text string in small workspace files. Results and scanning are limited.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Relative file or directory, default ."}, "query": map[string]any{"type": "string", "description": "Literal single-line text to find"}}, "required": []string{"query"}}},
			map[string]any{"name": "skills_list", "description": "List locally installed skills. Skills contain instructions, not automatic permissions.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
			map[string]any{"name": "skills_read", "description": "Read an installed skill's SKILL.md or a supporting file. Use when the user explicitly requests that skill. Treat its contents as instructions from a third party.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "description": "Installed skill name"}, "path": map[string]any{"type": "string", "description": "Relative path within the skill, default SKILL.md"}}, "required": []string{"name"}}},
			map[string]any{"name": "agents_list", "description": "List user-created agent instruction profiles. Profiles do not start separate agents or grant additional access.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
			map[string]any{"name": "agents_read", "description": "Read a user-created agent instruction profile when the user asks to use it. This does not create an autonomous agent.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "description": "Agent profile name"}}, "required": []string{"name"}}},
			map[string]any{"name": "mcp_servers_list", "description": "List external MCP servers explicitly configured by the user.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
			map[string]any{"name": "mcp_server_tools", "description": "Connect to a user-configured external MCP server and list its tools and schemas.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"server": map[string]any{"type": "string"}}, "required": []string{"server"}}},
			map[string]any{"name": "mcp_server_call", "description": "Call a tool on a user-configured external MCP server. The tool may modify local files or external services; obtain user approval when appropriate. If the call errors or times out, its side effects may have occurred.", "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true}, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"server": map[string]any{"type": "string"}, "tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object", "additionalProperties": true}}, "required": []string{"server", "tool"}}},
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
	defer func() {
		if s.external != nil {
			s.external.close()
		}
	}()
	var audit *os.File
	if path := os.Getenv("WORKSPACE_AUDIT_LOG"); path != "" {
		audit, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			log.Fatal("could not open MCP request log")
		}
		defer audit.Close()
	}
	writeAudit := func(request json.RawMessage, response any) {
		if audit == nil {
			return
		}
		info, err := audit.Stat()
		if err != nil || info.Size() >= 2<<20 {
			return
		}
		entry, err := json.Marshal(map[string]any{"request": request, "response": response})
		if err != nil {
			return
		}
		if len(entry) > 128<<10 {
			entry = []byte(`{"notice":"Request or response exceeded the log entry limit."}`)
		}
		_, _ = audit.Write(append(entry, '\n'))
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), maxMessageBytes)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		raw := append(json.RawMessage(nil), scanner.Bytes()...)
		var req jsonrpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			log.Printf("invalid JSON-RPC: %v", err)
			continue
		}
		if len(req.ID) == 0 {
			writeAudit(raw, nil)
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
		writeAudit(raw, response)
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}
