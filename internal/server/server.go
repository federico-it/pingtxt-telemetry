// Package server receives the hourly reports of pingtxt hubs and serves the
// totals to the owner.
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/federico-it/pingtxt-telemetry/internal/ratelimit"
	"github.com/federico-it/pingtxt-telemetry/internal/store"
)

// Config are the server settings, read from the environment by main.
type Config struct {
	// Window is how recent a report must be for a hub to count as live.
	Window time.Duration
	// TrustProxy reads the client IP from X-Forwarded-For (for the rate limit).
	TrustProxy bool
	// ClientIPHeader names a header that holds the client IP, set by a CDN
	// in front (CF-Connecting-IP for Cloudflare). Empty = not used.
	ClientIPHeader string
	// IPRate is how many reports one IP may send per minute.
	IPRate int
}

type Server struct {
	cfg     Config
	store   *store.Store
	log     *slog.Logger
	version string
	limit   *ratelimit.Limiter
	now     func() time.Time

	cacheMu  sync.Mutex
	cache    *Stats
	cachedAt time.Time
}

func New(cfg Config, st *store.Store, log *slog.Logger, version string) *Server {
	if cfg.Window <= 0 {
		cfg.Window = 2 * time.Hour
	}
	return &Server{
		cfg:     cfg,
		store:   st,
		log:     log,
		version: version,
		// Many hubs can sit behind one address (NAT, hosting providers).
		limit: ratelimit.New(cfg.IPRate, max(cfg.IPRate, 10)),
		now:   time.Now,
	}
}

//go:embed index.html
var indexHTML []byte

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/report", s.report)
	mux.HandleFunc("GET /v1/stats", s.stats)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(indexHTML)
	})
	return mux
}

// health answers 200 with {"status":"ok"} while the database answers,
// 503 with {"status":"error"} otherwise.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := s.store.Ping(r.Context()); err != nil {
		s.log.Error("health", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "version": s.version})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version})
}

// Sweep frees the rate limit buckets that are full again.
func (s *Server) Sweep() { s.limit.Sweep() }

// Instance ids are made by the hub with crypto/rand.Text (26 base32
// characters); other random ids up to 64 characters are accepted too.
var (
	idRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,40}$`)
)

const maxCount = 10_000_000

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	if !s.limit.Allow(s.clientIP(r), 1) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "too many reports")
		return
	}
	var in struct {
		ID       string `json:"id"`
		Version  string `json:"version"`
		Monitors int    `json:"monitors"`
		Agents   int    `json:"agents"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !idRe.MatchString(in.ID) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if in.Monitors < 0 || in.Monitors > maxCount || in.Agents < 0 || in.Agents > maxCount {
		writeErr(w, http.StatusBadRequest, "invalid counts")
		return
	}
	if !versionRe.MatchString(in.Version) {
		in.Version = "unknown"
	}
	rep := store.Report{ID: in.ID, Version: in.Version, Monitors: in.Monitors, Agents: in.Agents}
	if err := s.store.Save(r.Context(), rep, s.now()); err != nil {
		s.log.Error("save report", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Stats is the answer of GET /v1/stats.
type Stats struct {
	Live        store.Live           `json:"live"`
	WindowHours float64              `json:"window_hours"`
	Versions    []store.VersionCount `json:"versions"`
	Totals      store.Totals         `json:"totals"`
	History     []store.Day          `json:"history"` // last 90 days, oldest first
	At          int64                `json:"at"`      // unix ms
}

// stats is public: totals only, nothing about a single hub. The answer is
// cached for a minute so the page can be shared without loading the database.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	s.cacheMu.Lock()
	if s.cache == nil || s.now().Sub(s.cachedAt) >= time.Minute {
		st, err := s.Snapshot(r.Context())
		if err != nil {
			s.cacheMu.Unlock()
			s.log.Error("stats", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		s.cache, s.cachedAt = st, s.now()
	}
	st := s.cache
	s.cacheMu.Unlock()
	w.Header().Set("Cache-Control", "public, max-age=60")
	// Other sites (pingtxt.com) may show the numbers.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, st)
}

// Snapshot reads all the numbers.
func (s *Server) Snapshot(ctx context.Context) (*Stats, error) {
	now := s.now()
	st := &Stats{WindowHours: s.cfg.Window.Hours(), At: now.UnixMilli()}
	var err error
	if st.Live, err = s.store.Live(ctx, now, s.cfg.Window); err != nil {
		return nil, err
	}
	if st.Versions, err = s.store.Versions(ctx, now, s.cfg.Window); err != nil {
		return nil, err
	}
	if st.Totals, err = s.store.Totals(ctx, now); err != nil {
		return nil, err
	}
	if st.History, err = s.store.History(ctx, 90); err != nil {
		return nil, err
	}
	return st, nil
}

// Tick records today's live totals and drops hubs gone for longer than keep.
// main calls it every few minutes.
func (s *Server) Tick(ctx context.Context, keep time.Duration) error {
	now := s.now()
	l, err := s.store.Live(ctx, now, s.cfg.Window)
	if err != nil {
		return err
	}
	if err := s.store.RecordDay(ctx, now, l); err != nil {
		return err
	}
	n, err := s.store.Prune(ctx, now.Add(-keep))
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Info("pruned old hubs", "count", n)
	}
	return nil
}

// clientIP is the address the rate limit counts. Behind a CDN the last
// X-Forwarded-For entry is the CDN's, so ClientIPHeader comes first.
func (s *Server) clientIP(r *http.Request) string {
	if h := s.cfg.ClientIPHeader; h != "" {
		if ip := strings.TrimSpace(r.Header.Get(h)); net.ParseIP(ip) != nil {
			return ip
		}
	}
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// The proxy appends the address it saw: take the last one.
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
