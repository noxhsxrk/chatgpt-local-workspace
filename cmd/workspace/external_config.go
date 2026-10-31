package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const externalKeychainService = "chatgpt-local-workspace.external-mcp"

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var tokenAccountPattern = regexp.MustCompile(`^mcp-[a-f0-9]{32}$`)

type externalServer struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Transport    string   `json:"transport"`
	Command      string   `json:"command,omitempty"`
	Args         []string `json:"args,omitempty"`
	Env          []string `json:"env,omitempty"`
	URL          string   `json:"url,omitempty"`
	HasToken     bool     `json:"has_token,omitempty"`
	TokenAccount string   `json:"token_account,omitempty"`
	Revision     int64    `json:"revision"`
}

func externalRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chatgpt-local-workspace", "mcp-servers"), nil
}
func validateExternal(c externalServer) error {
	if !validSkillName(c.Name) {
		return errors.New("server name must use lowercase letters, numbers, and single hyphens")
	}
	if len(c.Description) > 1024 {
		return errors.New("description is too long")
	}
	if c.HasToken != (c.TokenAccount != "") || (c.HasToken && !tokenAccountPattern.MatchString(c.TokenAccount)) {
		return errors.New("invalid token configuration")
	}
	switch c.Transport {
	case "stdio":
		if strings.TrimSpace(c.Command) == "" || len(c.Command) > 1024 || strings.ContainsRune(c.Command, 0) || len(c.Args) > 64 || len(c.Env) > 32 {
			return errors.New("invalid stdio command, arguments, or environment")
		}
		for _, arg := range c.Args {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return errors.New("invalid argument")
			}
		}
		for _, name := range c.Env {
			if !envNamePattern.MatchString(name) || name == "CONTROL_PLANE_API_KEY" || strings.HasPrefix(name, "WORKSPACE_") {
				return errors.New("invalid or restricted environment variable")
			}
		}
		if c.HasToken {
			return errors.New("bearer authentication is available only for HTTP servers")
		}
	case "http":
		u, err := url.Parse(c.URL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return errors.New("enter a valid URL without embedded credentials or fragment")
		}
		if u.Scheme != "https" {
			ip := net.ParseIP(u.Hostname())
			if u.Scheme != "http" || !(strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())) {
				return errors.New("HTTP is permitted only for loopback; use HTTPS for remote servers")
			}
		}
	default:
		return errors.New("transport must be stdio or http")
	}
	return nil
}
func saveExternal(root string, c externalServer) error {
	if err := validateExternal(c); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	c.Revision = time.Now().UnixNano()
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".mcp-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(root, c.Name+".json"))
}
func readExternal(root, name string) (externalServer, error) {
	if !validSkillName(name) {
		return externalServer{}, errors.New("invalid server name")
	}
	data, err := readSmallSkillFile(filepath.Join(root, name+".json"))
	if err != nil {
		return externalServer{}, err
	}
	var c externalServer
	if err = json.Unmarshal(data, &c); err != nil {
		return externalServer{}, err
	}
	if err = validateExternal(c); err != nil {
		return externalServer{}, err
	}
	if c.Name != name {
		return externalServer{}, errors.New("server name does not match file")
	}
	return c, nil
}
func listExternal(root string) ([]externalServer, error) {
	result := []externalServer{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if c, err := readExternal(root, name); err == nil {
			result = append(result, c)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func removeExternal(root, name string) error {
	if !validSkillName(name) {
		return errors.New("invalid server name")
	}
	return os.Remove(filepath.Join(root, name+".json"))
}
