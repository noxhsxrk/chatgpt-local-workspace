package main

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEstimateMCPUsage(t *testing.T) {
	log := `{"request":{"method":"initialize"},"response":{"result":{}}}
{"request":{"method":"tools/call","params":{"arguments":{"x":1}}},"response":{"result":"abcd"}}
{"request":{"method":"notifications/initialized"},"response":null}`
	got, err := estimateMCPUsage(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	if got.Calls != 1 || got.InputBytes != 6 || got.OutputBytes != 7 || got.InputTokens != 2 || got.OutputTokens != 2 {
		t.Fatalf("unexpected estimate: %+v", got)
	}
	if math.Abs(got.EstimatedUSD-0.0000012) > 1e-12 {
		t.Fatalf("incorrect price: %v", got.EstimatedUSD)
	}
}
func TestEstimateFlagsOmittedEntries(t *testing.T) {
	got, err := estimateMCPUsage(strings.NewReader("{\"notice\":\"entry omitted\"}\n{broken\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Incomplete {
		t.Fatal("incomplete log was not flagged")
	}
}
func TestUsageEndpointRequiresTokenAndLogging(t *testing.T) {
	m := &webManager{token: "secret"}
	req := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/usage", nil)
	w := httptest.NewRecorder()
	m.handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatal("unauthorized usage access")
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1:1234/api/usage", nil)
	req.Header.Set("X-Workspace-Token", "secret")
	w = httptest.NewRecorder()
	m.handler().ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("unexpected response: %s", w.Body)
	}
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.auditPath = f.Name()
	w = httptest.NewRecorder()
	m.handler().ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("unexpected response: %s", w.Body)
	}
}
