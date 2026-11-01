package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/federico-it/pingtxt-telemetry/internal/store"
)

func newServer(t *testing.T) (*Server, *time.Time) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Config{Window: 2 * time.Hour, IPRate: 1000}, st, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

func post(t *testing.T, h http.Handler, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/report", strings.NewReader(body)))
	return w.Code
}

func getStats(t *testing.T, s *Server) (int, Stats) {
	t.Helper()
	s.cacheMu.Lock()
	s.cache = nil // tests move the clock: no cached answer
	s.cacheMu.Unlock()
	r := httptest.NewRequest("GET", "/v1/stats", nil)
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var st Stats
	_ = json.NewDecoder(w.Body).Decode(&st)
	return w.Code, st
}

func TestReportsAndStats(t *testing.T) {
	s, now := newServer(t)
	h := s.Handler()

	for _, b := range []string{
		`{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","version":"v0.4.0","monitors":10,"agents":2}`,
		`{"id":"BBBBBBBBBBBBBBBBBBBBBBBBBB","version":"v0.4.0","monitors":5,"agents":0}`,
		`{"id":"CCCCCCCCCCCCCCCCCCCCCCCCCC","version":"main-abc1234","monitors":1,"agents":1}`,
		// Same hub again: replaces its report.
		`{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","version":"v0.4.1","monitors":12,"agents":3,"extra":"ignored"}`,
	} {
		if c := post(t, h, b); c != 204 {
			t.Fatalf("%s: %d", b, c)
		}
	}
	for _, b := range []string{
		`not json`,
		`{"id":"short","version":"v1","monitors":1,"agents":1}`,
		`{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA<script>","monitors":1}`,
		`{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","monitors":-1,"agents":0}`,
		`{"id":"` + strings.Repeat("A", 5000) + `"}`,
	} {
		if c := post(t, h, b); c != 400 {
			t.Errorf("%.40s: %d", b, c)
		}
	}

	c, st := getStats(t, s)
	if c != 200 || st.Live != (store.Live{Instances: 3, Monitors: 18, Agents: 4}) {
		t.Fatalf("live: %d %+v", c, st.Live)
	}
	if len(st.Versions) != 3 || st.Versions[0].Version != "v0.4.1" || st.Versions[0].Instances != 1 {
		t.Fatalf("versions: %+v", st.Versions)
	}
	if st.Totals.Ever != 3 || st.Totals.New7d != 3 || st.Totals.Seen30d != 3 {
		t.Fatalf("totals: %+v", st.Totals)
	}

	// Daily peak keeps the highest live numbers of the day.
	if err := s.Tick(context.Background(), 180*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	// Three hours later only a fresh report counts as live.
	*now = now.Add(3 * time.Hour)
	post(t, h, `{"id":"BBBBBBBBBBBBBBBBBBBBBBBBBB","version":"v0.4.1","monitors":6,"agents":1}`)
	if err := s.Tick(context.Background(), 180*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	_, st = getStats(t, s)
	if st.Live != (store.Live{Instances: 1, Monitors: 6, Agents: 1}) {
		t.Fatalf("live later: %+v", st.Live)
	}
	if len(st.History) != 1 || st.History[0].Day != "2026-11-01" || st.History[0].Instances != 3 || st.History[0].Monitors != 18 {
		t.Fatalf("history: %+v", st.History)
	}

	// Hubs gone longer than the retention are dropped.
	*now = now.Add(200 * 24 * time.Hour)
	if err := s.Tick(context.Background(), 180*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, st = getStats(t, s); st.Totals.Ever != 0 || len(st.History) != 2 {
		t.Fatalf("after prune: %+v", st)
	}
}

func TestStatsCached(t *testing.T) {
	s, now := newServer(t)
	h := s.Handler()
	get := func() Stats {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/stats", nil))
		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("no CORS header")
		}
		var st Stats
		_ = json.NewDecoder(w.Body).Decode(&st)
		return st
	}
	get()
	post(t, h, `{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","version":"v1","monitors":1,"agents":1}`)
	if st := get(); st.Live.Instances != 0 {
		t.Fatalf("not cached: %+v", st.Live)
	}
	*now = now.Add(time.Minute)
	if st := get(); st.Live.Instances != 1 {
		t.Fatalf("stale: %+v", st.Live)
	}
}

func TestRateLimit(t *testing.T) {
	s, _ := newServer(t)
	s.cfg.TrustProxy = true
	s.limit = New(Config{IPRate: 2}, nil, nil, "").limit
	h := s.Handler()
	body := `{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","version":"v1","monitors":1,"agents":1}`
	codes := []int{}
	for range 12 {
		r := httptest.NewRequest("POST", "/v1/report", strings.NewReader(body))
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		codes = append(codes, w.Code)
	}
	if codes[0] != 204 || codes[11] != 429 {
		t.Fatalf("codes: %v", codes)
	}
}

func TestPages(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	for path, want := range map[string]int{"/": 200, "/healthz": 200, "/nope": 404} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}
