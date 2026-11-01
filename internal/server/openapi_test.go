package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestOpenAPIRoutes checks that every operation in docs/openapi.json (the
// schema uploaded to Cloudflare) is a route the service serves, and that
// every route is documented: a missing route would be blocked at the edge.
func TestOpenAPIRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.0.") {
		t.Fatalf("openapi %q: Cloudflare takes 3.0.x", doc.OpenAPI)
	}
	s, _ := newServer(t)
	h := s.Handler()
	documented := map[string]bool{}
	for path, ops := range doc.Paths {
		for method := range ops {
			m := strings.ToUpper(method)
			documented[m+" "+path] = true
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(m, path, strings.NewReader("{}")))
			if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
				t.Errorf("%s %s is documented but not served (%d)", m, path, w.Code)
			}
		}
	}
	// Keep in sync with Handler.
	for _, r := range []string{"GET /", "GET /healthz", "POST /v1/report", "GET /v1/stats"} {
		if !documented[r] {
			t.Errorf("%s is served but missing from docs/openapi.json", r)
		}
	}
}
