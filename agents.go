package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type agentProfile struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}
type agentSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func agentRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chatgpt-local-workspace", "agents"), nil
}
func validAgent(a agentProfile) error {
	if !validSkillName(a.Name) {
		return errors.New("agent name must use lowercase letters, numbers, and single hyphens")
	}
	if strings.TrimSpace(a.Description) == "" || len(a.Description) > 1024 {
		return errors.New("description is required and must be at most 1024 bytes")
	}
	if strings.TrimSpace(a.Instructions) == "" || len(a.Instructions) > 32<<10 {
		return errors.New("instructions are required and must be at most 32 KiB")
	}
	return nil
}
func saveAgent(root string, a agentProfile) error {
	if err := validAgent(a); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".agent-*.json")
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
	return os.Rename(f.Name(), filepath.Join(root, a.Name+".json"))
}
func readAgent(root, name string) (agentProfile, error) {
	if !validSkillName(name) {
		return agentProfile{}, errors.New("invalid agent name")
	}
	data, err := readSmallSkillFile(filepath.Join(root, name+".json"))
	if err != nil {
		return agentProfile{}, err
	}
	var a agentProfile
	if err = json.Unmarshal(data, &a); err != nil {
		return agentProfile{}, err
	}
	if err = validAgent(a); err != nil {
		return agentProfile{}, err
	}
	if a.Name != name {
		return agentProfile{}, errors.New("agent name does not match file")
	}
	return a, nil
}
func listAgents(root string) ([]agentSummary, error) {
	result := []agentSummary{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		a, err := readAgent(root, name)
		if err == nil {
			result = append(result, agentSummary{Name: a.Name, Description: a.Description})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func removeAgent(root, name string) error {
	if !validSkillName(name) {
		return errors.New("invalid agent name")
	}
	return os.Remove(filepath.Join(root, name+".json"))
}
