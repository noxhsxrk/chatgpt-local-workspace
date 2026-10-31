package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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
