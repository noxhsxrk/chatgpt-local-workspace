package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReleaseVersionComparison(t *testing.T) {
	for _, tt := range []struct {
		latest, current string
		want            bool
		bad             bool
	}{{"v0.3.0", "0.2.9", true, false}, {"v0.2.10", "0.2.9", true, false}, {"v0.2.5", "0.2.6", false, false}, {"v0.2.6", "0.2.6", false, false}, {"v1.0.0-rc.1", "0.2.6", false, true}, {"invalid", "0.2.6", false, true}} {
		got, err := isNewerRelease(tt.latest, tt.current)
		if (err != nil) != tt.bad || got != tt.want {
			t.Errorf("%q %q: %v, %v", tt.latest, tt.current, got, err)
		}
	}
}
func TestFetchUpdateOnlyStableReleases(t *testing.T) {
	client := marketClient([]byte(`{"tag_name":"v1.2.3","draft":false,"prerelease":false}`))
	info, err := fetchUpdate(context.Background(), client, "https://api.github.com/example", "1.1.9")
	if err != nil || !info.Available || info.Latest != "1.2.3" {
		t.Fatalf("update: %+v %v", info, err)
	}
	client = marketClient([]byte(`{"tag_name":"v1.2.3","prerelease":true}`))
	if _, err = fetchUpdate(context.Background(), client, "https://api.github.com/example", "1.1.9"); err == nil {
		t.Fatal("accepted prerelease")
	}
}
func TestUpdateEndpointRequiresToken(t *testing.T) {
	m := &webManager{token: "secret"}
	req := httptest.NewRequest(http.MethodGet, "/api/update", nil)
	rec := httptest.NewRecorder()
	m.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestUpdateRefreshBypassesCache(t *testing.T) {
	original := releaseHTTPClient
	t.Cleanup(func() { releaseHTTPClient = original })
	calls := 0
	releaseHTTPClient = &http.Client{Transport: marketRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		tag := "v" + appVersion
		if calls > 1 {
			tag = "v999.0.0"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"` + tag + `"}`)), Header: make(http.Header)}, nil
	})}
	checker := &updateChecker{}
	first, err := checker.check(context.Background(), false)
	if err != nil || first.Available {
		t.Fatalf("first check: %+v %v", first, err)
	}
	cached, err := checker.check(context.Background(), false)
	if err != nil || cached.Available || calls != 1 {
		t.Fatalf("cached check: %+v %v calls=%d", cached, err, calls)
	}
	fresh, err := checker.check(context.Background(), true)
	if err != nil || !fresh.Available || fresh.Latest != "999.0.0" || calls != 2 {
		t.Fatalf("forced check: %+v %v calls=%d", fresh, err, calls)
	}
}
