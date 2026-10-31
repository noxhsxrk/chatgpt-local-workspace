package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

const maxSkillFiles = 512
const maxSkillBytes = 8 << 20

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type skillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func skillRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chatgpt-local-workspace", "skills"), nil
}
func validSkillName(name string) bool { return len(name) <= 64 && skillNamePattern.MatchString(name) }

func openSkillFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("could not open file")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("not a regular file")
	}
	return f, nil
}

func readSmallSkillFile(path string) ([]byte, error) {
	f, err := openSkillFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("skill file too large")
	}
	return data, nil
}
func parseSkillInfo(dir string, data []byte) (skillInfo, error) {
	name := filepath.Base(dir)
	if !validSkillName(name) {
		return skillInfo{}, errors.New("skill folder name must use lowercase letters, numbers, and single hyphens")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 4 || lines[0] != "---" {
		return skillInfo{}, errors.New("SKILL.md must start with YAML frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return skillInfo{}, errors.New("SKILL.md frontmatter is not closed")
	}
	var declared, description string
	for i := 1; i < end; i++ {
		line := lines[i]
		if strings.HasPrefix(line, "name:") {
			declared = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), "\"'")
		}
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			if description == "|" || description == ">" || description == "|-" || description == ">-" {
				var parts []string
				for j := i + 1; j < end; j++ {
					if len(lines[j]) > 0 && lines[j][0] != ' ' && lines[j][0] != '\t' {
						break
					}
					if v := strings.TrimSpace(lines[j]); v != "" {
						parts = append(parts, v)
					}
				}
				description = strings.Join(parts, " ")
			} else {
				description = strings.Trim(description, "\"'")
			}
		}
	}
	if declared != name {
		return skillInfo{}, errors.New("SKILL.md name must match its folder")
	}
	if description == "" || len(description) > 4096 {
		return skillInfo{}, errors.New("SKILL.md requires a description")
	}
	return skillInfo{Name: name, Description: description}, nil
}
func listSkills(root string) ([]skillInfo, error) {
	result := []skillInfo{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validSkillName(entry.Name()) {
			continue
		}
		p := filepath.Join(root, entry.Name(), "SKILL.md")
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			continue
		}
		data, err := readSmallSkillFile(p)
		if err != nil {
			continue
		}
		skill, err := parseSkillInfo(filepath.Join(root, entry.Name()), data)
		if err == nil {
			result = append(result, skill)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func installSkill(root, source string) (skillInfo, error) {
	if source == "~" || strings.HasPrefix(source, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return skillInfo{}, err
		}
		if source == "~" {
			source = home
		} else {
			source = filepath.Join(home, source[2:])
		}
	}
	if !filepath.IsAbs(source) {
		return skillInfo{}, errors.New("skill folder must be an absolute path")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return skillInfo{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return skillInfo{}, errors.New("skill source must be a regular directory")
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return skillInfo{}, err
	}
	name := filepath.Base(source)
	if !validSkillName(name) {
		return skillInfo{}, errors.New("invalid skill folder name")
	}
	skillFile := filepath.Join(source, "SKILL.md")
	info, err = os.Lstat(skillFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return skillInfo{}, errors.New("skill folder must contain a regular SKILL.md")
	}
	data, err := readSmallSkillFile(skillFile)
	if err != nil {
		return skillInfo{}, err
	}
	skill, err := parseSkillInfo(source, data)
	if err != nil {
		return skillInfo{}, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return skillInfo{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return skillInfo{}, err
	}
	if rel, e := filepath.Rel(source, root); e == nil && (rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return skillInfo{}, errors.New("skill source cannot contain the managed skill library")
	}
	lock := filepath.Join(root, "."+name+".lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return skillInfo{}, errors.New("skill is being installed")
	}
	_ = f.Close()
	defer os.Remove(lock)
	dest := filepath.Join(root, name)
	if _, err = os.Lstat(dest); err == nil {
		return skillInfo{}, errors.New("skill already exists; remove it before reinstalling")
	} else if !os.IsNotExist(err) {
		return skillInfo{}, err
	}
	temp, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return skillInfo{}, err
	}
	defer os.RemoveAll(temp)
	files, total := 0, int64(0)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("skill packages cannot contain symbolic links")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(temp, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("skill packages may only contain regular files")
		}
		files++
		total += info.Size()
		if files > maxSkillFiles || total > maxSkillBytes {
			return errors.New("skill package exceeds 512 files or 8 MiB")
		}
		in, err := openSkillFile(path)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0600)
		if info.Mode()&0111 != 0 {
			mode = 0700
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, info.Size()+1))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != info.Size() {
			return errors.New("skill changed while being installed")
		}
		return nil
	})
	if err != nil {
		return skillInfo{}, err
	}
	// Re-validate the copied metadata before publishing the managed copy.
	copied, err := readSmallSkillFile(filepath.Join(temp, "SKILL.md"))
	if err != nil {
		return skillInfo{}, err
	}
	if _, err = parseSkillInfo(filepath.Join(root, name), copied); err != nil {
		return skillInfo{}, err
	}
	if err = os.Rename(temp, dest); err != nil {
		return skillInfo{}, err
	}
	return skill, nil
}
func readSkillFile(root, name, path string) (string, error) {
	if !validSkillName(name) {
		return "", errors.New("invalid skill name")
	}
	if path == "" {
		path = "SKILL.md"
	}
	if filepath.IsAbs(path) {
		return "", errors.New("absolute paths are not allowed")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the skill")
	}
	base := filepath.Join(root, name)
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("skill not found")
	}
	cur := base
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err = os.Lstat(cur)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symbolic links are not allowed")
		}
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return "", errors.New("skill file is not a regular file or is too large")
	}
	f, err := openSkillFile(cur)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxFileBytes {
		return "", errors.New("skill file too large")
	}
	return string(data), nil
}
func removeSkill(root, name string) error {
	if !validSkillName(name) {
		return errors.New("invalid skill name")
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid skill directory")
	}
	return os.RemoveAll(path)
}
