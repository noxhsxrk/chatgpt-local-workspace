package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var releasePattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var releaseHTTPClient = &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
	return errors.New("release API redirects are not allowed")
}}

type updateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
}

func releaseVersion(s string) ([3]uint64, error) {
	var result [3]uint64
	match := releasePattern.FindStringSubmatch(s)
	if match == nil {
		return result, errors.New("invalid release version")
	}
	for i := range result {
		v, e := strconv.ParseUint(match[i+1], 10, 64)
		if e != nil {
			return result, e
		}
		result[i] = v
	}
	return result, nil
}
func isNewerRelease(latest, current string) (bool, error) {
	a, e := releaseVersion(latest)
	if e != nil {
		return false, e
	}
	b, e := releaseVersion(current)
	if e != nil {
		return false, e
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i], nil
		}
	}
	return false, nil
}
func fetchUpdate(ctx context.Context, client *http.Client, endpoint, current string) (updateInfo, error) {
	result := updateInfo{Current: current}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "chatgpt-local-workspace-update-check")
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("release service returned HTTP %d", resp.StatusCode)
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return result, err
	}
	if release.Draft || release.Prerelease {
		return result, errors.New("release is not stable")
	}
	newer, err := isNewerRelease(release.Tag, current)
	if err != nil {
		return result, err
	}
	result.Latest = strings.TrimPrefix(release.Tag, "v")
	result.Available = newer
	return result, nil
}

type updateChecker struct {
	mu      sync.Mutex
	info    updateInfo
	checked time.Time
}

func (c *updateChecker) check(ctx context.Context) (updateInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.checked.IsZero() && time.Since(c.checked) < 6*time.Hour {
		return c.info, nil
	}
	info, err := fetchUpdate(ctx, releaseHTTPClient, "https://api.github.com/repos/noxhsxrk/chatgpt-local-workspace/releases/latest", appVersion)
	if err != nil {
		return updateInfo{}, err
	}
	c.info = info
	c.checked = time.Now()
	return info, nil
}

var appUpdates updateChecker
