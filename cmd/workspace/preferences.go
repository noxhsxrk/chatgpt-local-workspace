package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const keychainService = "chatgpt-local-workspace.runtime-api-key"
const keychainAccount = "runtime"

// LocalAuthentication uses Touch ID when available and permits the system
// password fallback. The secret is returned only after successful verification.
func authenticateDevice() error {
	const script = `import LocalAuthentication
import Foundation
import Dispatch
let context = LAContext()
var error: NSError?
guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) else { exit(1) }
let done = DispatchSemaphore(value: 0)
var verified = false
context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: "Show your saved tunnel API key") { success, _ in
    verified = success
    done.signal()
}
if done.wait(timeout: .now() + 90) == .timedOut { exit(1) }
exit(verified ? 0 : 1)`
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/swift", "-e", script).Run()
}

type savedPreferences struct {
	Tunnel    string `json:"tunnel"`
	Workspace string `json:"workspace"`
}

var preferencesMu sync.Mutex

func connectionsPath() (string, error) {
	p, err := preferencesPath()
	return filepath.Join(filepath.Dir(p), "connections.json"), err
}
func readConnections() ([]savedPreferences, error) {
	p, err := connectionsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		t, w := loadWebPreferences()
		if tunnelPattern.MatchString(t) && w != "" {
			return []savedPreferences{{Tunnel: t, Workspace: w}}, nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []savedPreferences
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}
func updateConnections(item savedPreferences, remove bool) error {
	preferencesMu.Lock()
	defer preferencesMu.Unlock()
	items, err := readConnections()
	if err != nil {
		return err
	}
	updated := make([]savedPreferences, 0, len(items)+1)
	found := false
	for _, v := range items {
		if v.Tunnel == item.Tunnel {
			found = true
			if !remove {
				updated = append(updated, item)
			}
		} else {
			updated = append(updated, v)
		}
	}
	if !found && !remove {
		updated = append(updated, item)
	}
	p, err := connectionsPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".connections-*")
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
	return os.Rename(f.Name(), p)
}

func preferencesPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chatgpt-local-workspace", "settings.json"), nil
}
func loadWebPreferences() (string, string) {
	p, err := preferencesPath()
	if err != nil {
		return "", ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", ""
	}
	var v savedPreferences
	if json.Unmarshal(data, &v) != nil {
		return "", ""
	}
	return v.Tunnel, v.Workspace
}
func saveWebPreferences(tunnel, workspace string) error {
	p, err := preferencesPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(savedPreferences{Tunnel: tunnel, Workspace: workspace})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
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
	return os.Rename(name, p)
}
func loadKeychainKey() (string, error) {
	return loadKeychainAccount(keychainAccount)
}
func loadKeychainAccount(account string) (string, error) {
	return loadKeychainEntry(keychainService, account)
}
func loadKeychainEntry(service, account string) (string, error) {
	output, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}
func saveKeychainKey(key string) error {
	return saveKeychainAccount(keychainAccount, key)
}
func saveKeychainAccount(account, key string) error {
	return saveKeychainEntry(keychainService, account, key)
}
func saveKeychainEntry(service, account, key string) error {
	// Pass the encoded secret over stdin, never in a command-line argument.
	cmd := exec.Command("/usr/bin/security", "-i")
	command := "add-generic-password -U -s " + service + " -a " + account + " -X " + hex.EncodeToString([]byte(key)) + "\n"
	cmd.Stdin = strings.NewReader(command)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return err
	}
	if strings.Contains(output.String(), "returned ") {
		return errors.New("keychain update failed")
	}
	got, err := loadKeychainEntry(service, account)
	if err != nil {
		return err
	}
	if got != key {
		return errors.New("keychain verification failed")
	}
	return nil
}
