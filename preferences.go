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
	output, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", keychainService, "-a", keychainAccount, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}
func saveKeychainKey(key string) error {
	// Pass the encoded secret over stdin, never in a command-line argument.
	cmd := exec.Command("/usr/bin/security", "-i")
	command := "add-generic-password -U -s " + keychainService + " -a " + keychainAccount + " -X " + hex.EncodeToString([]byte(key)) + "\n"
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
	got, err := loadKeychainKey()
	if err != nil {
		return err
	}
	if got != key {
		return errors.New("keychain verification failed")
	}
	return nil
}
