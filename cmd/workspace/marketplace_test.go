package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type marketRoundTrip func(*http.Request) (*http.Response, error)

func (f marketRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func marketClient(body []byte) *http.Client {
	return &http.Client{Transport: marketRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
}
func marketZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, body := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestMarketplaceSearchAndInstall(t *testing.T) {
	client := marketClient([]byte(`{"skills":[{"id":"owner/repo/example-skill","name":"example-skill","source":"owner/repo","installs":20},{"name":"invalid","source":"../../bad"}]}`))
	got, err := marketplaceSearch(context.Background(), client, "https://skills.sh", "example")
	if err != nil || len(got) != 1 || got[0].Source != "owner/repo" {
		t.Fatalf("search %+v %v", got, err)
	}
	data := marketZip(t, map[string]string{"repo-head/skills/example-skill/SKILL.md": "---\nname: example-skill\ndescription: Example\n---\nHello", "repo-head/skills/example-skill/references/a.md": "reference"})
	root := filepath.Join(t.TempDir(), "skills")
	s, err := installMarketplaceSkill(context.Background(), marketClient(data), "https://codeload.github.com", root, got[0].Source, got[0].Name)
	if err != nil || s.Name != "example-skill" {
		t.Fatalf("install %+v %v", s, err)
	}
	b, err := os.ReadFile(filepath.Join(root, "example-skill", "references", "a.md"))
	if err != nil || string(b) != "reference" {
		t.Fatalf("reference %q %v", b, err)
	}
	if _, err = installMarketplaceSkill(context.Background(), marketClient(data), "https://codeload.github.com", root, "../secret", "example-skill"); err == nil {
		t.Fatal("invalid source accepted")
	}
}
func TestMarketplaceRejectsAmbiguousSkill(t *testing.T) {
	data := marketZip(t, map[string]string{"repo/a/example-skill/SKILL.md": "a", "repo/b/example-skill/SKILL.md": "b"})
	_, err := installMarketplaceSkill(context.Background(), marketClient(data), "https://codeload.github.com", t.TempDir(), "owner/repo", "example-skill")
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected ambiguous error: %v", err)
	}
}
func TestMarketplaceRoutesRequireToken(t *testing.T) {
	m := &webManager{token: "secret"}
	for _, p := range []string{"/api/marketplace/search?q=go", "/api/marketplace/install"} {
		req, _ := http.NewRequest(http.MethodGet, p, nil)
		w := &testResponseWriter{header: make(http.Header)}
		m.handler().ServeHTTP(w, req)
		if w.status != http.StatusForbidden {
			t.Fatalf("%s status %d", p, w.status)
		}
	}
}

type testResponseWriter struct {
	header http.Header
	status int
}

func (w *testResponseWriter) Header() http.Header         { return w.header }
func (w *testResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *testResponseWriter) WriteHeader(n int)           { w.status = n }

func TestMarketplaceInstallsRootSkill(t *testing.T) {
	data := marketZip(t, map[string]string{"repo-head/SKILL.md": "---\nname: root-skill\ndescription: Root skill\n---\nInstructions", "repo-head/references/example.md": "example"})
	root := filepath.Join(t.TempDir(), "skills")
	_, err := installMarketplaceSkill(context.Background(), marketClient(data), "https://codeload.github.com", root, "owner/repo", "root-skill")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "root-skill", "references", "example.md")); err != nil {
		t.Fatal(err)
	}
}
func TestMarketplaceRejectsArchiveTraversal(t *testing.T) {
	data := marketZip(t, map[string]string{"repo/skills/example-skill/SKILL.md": "---\nname: example-skill\ndescription: Example\n---\n", "repo/skills/example-skill/../../escape": "bad"})
	_, err := installMarketplaceSkill(context.Background(), marketClient(data), "https://codeload.github.com", t.TempDir(), "owner/repo", "example-skill")
	if err == nil {
		t.Fatal("archive traversal allowed")
	}
}
