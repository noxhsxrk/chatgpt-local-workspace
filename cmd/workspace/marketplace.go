package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// skills.sh's public search endpoint is also used by its official CLI. It is
// separate from the authenticated v1 API and may change without notice.
var marketplaceClient = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 || req.URL.Scheme != "https" || (req.URL.Hostname() != "skills.sh" && req.URL.Hostname() != "www.skills.sh" && req.URL.Hostname() != "codeload.github.com") {
		return errors.New("unexpected marketplace redirect")
	}
	return nil
}}
var githubPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

type marketplaceSkill struct {
	ID       string `json:"id"`
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Installs int64  `json:"installs"`
}

func validMarketplaceSource(source string) bool {
	p := strings.Split(source, "/")
	return len(p) == 2 && githubPartPattern.MatchString(p[0]) && githubPartPattern.MatchString(p[1]) && p[0] != "." && p[0] != ".." && p[1] != "." && p[1] != ".."
}
func marketplaceSearch(ctx context.Context, client *http.Client, base, query string) ([]marketplaceSkill, error) {
	query = strings.TrimSpace(query)
	if len(query) < 2 || len(query) > 120 {
		return nil, errors.New("Enter a search term between 2 and 120 characters.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/search?"+url.Values{"q": {query}, "limit": {"30"}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Could not reach skills.sh.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills.sh returned HTTP %d.", resp.StatusCode)
	}
	var result struct {
		Skills []marketplaceSkill `json:"skills"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, errors.New("Invalid response from skills.sh.")
	}
	out := make([]marketplaceSkill, 0, len(result.Skills))
	for _, s := range result.Skills {
		if s.SkillID == "" {
			s.SkillID = s.Name
		}
		if validMarketplaceSource(s.Source) && validSkillName(s.SkillID) && len(s.ID) <= 300 && len(s.Name) <= 200 {
			out = append(out, s)
		}
		if len(out) == 30 {
			break
		}
	}
	return out, nil
}
func installMarketplaceSkill(ctx context.Context, client *http.Client, base, root, source, name string) (skillInfo, error) {
	if !validMarketplaceSource(source) || !validSkillName(name) {
		return skillInfo{}, errors.New("Invalid repository or skill name.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+source+"/zip/HEAD", nil)
	if err != nil {
		return skillInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return skillInfo{}, errors.New("Could not download the GitHub repository.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return skillInfo{}, fmt.Errorf("GitHub returned HTTP %d.", resp.StatusCode)
	}
	const maxArchive = 40 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return skillInfo{}, errors.New("Could not read the repository archive.")
	}
	if len(data) > maxArchive {
		return skillInfo{}, errors.New("Repository archive exceeds 40 MiB.")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return skillInfo{}, errors.New("Invalid repository archive.")
	}
	if len(z.File) > 50000 {
		return skillInfo{}, errors.New("Repository has too many files.")
	}
	// Repositories may contain platform-specific copies. Prefer the conventional
	// top-level skills/<name> directory; never choose between other duplicates.
	candidates := map[string]string{}
	var canonical string
	metadataBudget := int64(8 << 20)
	for _, f := range z.File {
		n := f.Name
		if path.Clean(n) != n || strings.HasPrefix(n, "/") || strings.Contains(n, "\\") || path.Base(n) != "SKILL.md" || !f.Mode().IsRegular() || f.UncompressedSize64 > maxFileBytes {
			continue
		}
		if int64(f.UncompressedSize64) > metadataBudget {
			return skillInfo{}, errors.New("Repository contains too much skill metadata to inspect.")
		}
		metadataBudget -= int64(f.UncompressedSize64)
		reader, e := f.Open()
		if e != nil {
			continue
		}
		contents, e := io.ReadAll(io.LimitReader(reader, maxFileBytes+1))
		_ = reader.Close()
		if e != nil || len(contents) > maxFileBytes {
			continue
		}
		info, e := parseSkillMetadata(contents)
		if e != nil {
			continue
		}
		dir := path.Dir(n)
		if path.Base(dir) != name && info.Name != name {
			continue
		}
		candidates[dir] = info.Name
		parts := strings.Split(dir, "/")
		if len(parts) == 3 && parts[1] == "skills" && parts[2] == name {
			canonical = dir
		}
	}
	var selected, installedName string
	if canonical != "" {
		selected = canonical
		installedName = candidates[canonical]
	} else if len(candidates) == 1 {
		for dir, skillName := range candidates {
			selected = dir
			installedName = skillName
		}
	} else if len(candidates) == 0 {
		return skillInfo{}, errors.New("Skill not found in the repository. It may have been moved or removed; check the source on GitHub.")
	} else {
		return skillInfo{}, errors.New("The repository contains multiple matching skills and no standard skills/<name> folder. Check the source on GitHub.")
	}
	temp, err := os.MkdirTemp("", "workspace-skill-")
	if err != nil {
		return skillInfo{}, err
	}
	defer os.RemoveAll(temp)
	folder := filepath.Join(temp, installedName)
	if err = os.Mkdir(folder, 0700); err != nil {
		return skillInfo{}, err
	}
	count := 0
	var total uint64
	for _, f := range z.File {
		if !strings.HasPrefix(f.Name, selected+"/") {
			continue
		}
		rel := strings.TrimPrefix(f.Name, selected+"/")
		if rel == "" {
			continue
		}
		clean := path.Clean(rel)
		if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.Contains(clean, "\\") {
			return skillInfo{}, errors.New("Invalid path in skill package.")
		}
		target := filepath.Join(folder, filepath.FromSlash(clean))
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return skillInfo{}, errors.New("Skill package contains a symbolic link.")
		}
		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0700); err != nil {
				return skillInfo{}, err
			}
			continue
		}
		if !mode.IsRegular() {
			return skillInfo{}, errors.New("Skill package contains a non-regular file.")
		}
		count++
		if f.UncompressedSize64 > maxSkillBytes || total > maxSkillBytes-f.UncompressedSize64 {
			return skillInfo{}, errors.New("Skill package exceeds 512 files or 8 MiB.")
		}
		total += f.UncompressedSize64
		if count > maxSkillFiles {
			return skillInfo{}, errors.New("Skill package exceeds 512 files or 8 MiB.")
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return skillInfo{}, err
		}
		in, e := f.Open()
		if e != nil {
			return skillInfo{}, e
		}
		out, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			in.Close()
			return skillInfo{}, e
		}
		n, e := io.Copy(out, io.LimitReader(in, int64(f.UncompressedSize64)+1))
		closeErr := out.Close()
		in.Close()
		if e != nil {
			return skillInfo{}, e
		}
		if closeErr != nil {
			return skillInfo{}, closeErr
		}
		if n != int64(f.UncompressedSize64) {
			return skillInfo{}, errors.New("Invalid file size in skill package.")
		}
	}
	return installSkill(root, folder)
}
