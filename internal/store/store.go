// Package store keeps the last report of every hub and one row of totals per
// day in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Report is what a hub sends once an hour.
type Report struct {
	ID       string
	Version  string
	Monitors int
	Agents   int
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS instances (
	id         TEXT PRIMARY KEY,
	version    TEXT NOT NULL,
	monitors   INTEGER NOT NULL,
	agents     INTEGER NOT NULL,
	first_seen INTEGER NOT NULL, -- unix seconds
	last_seen  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS instances_last_seen ON instances(last_seen);
CREATE TABLE IF NOT EXISTS daily (
	day       TEXT PRIMARY KEY, -- YYYY-MM-DD (UTC)
	instances INTEGER NOT NULL, -- highest live count seen that day
	monitors  INTEGER NOT NULL,
	agents    INTEGER NOT NULL
);
`

// Open opens (and creates) telemetry.db in dir.
func Open(dir string) (*Store, error) {
	dsn := "file:" + filepath.Join(dir, "telemetry.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; reads are tiny
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Save keeps r as the latest report of its hub.
func (s *Store) Save(ctx context.Context, r Report, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO instances (id, version, monitors, agents, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version, monitors = excluded.monitors,
			agents = excluded.agents, last_seen = excluded.last_seen`,
		r.ID, r.Version, r.Monitors, r.Agents, now.Unix(), now.Unix())
	return err
}

// Live are the totals of the hubs that reported within the window.
type Live struct {
	Instances int `json:"instances"`
	Monitors  int `json:"monitors"`
	Agents    int `json:"agents"`
}

// Live sums the hubs seen since now-window.
func (s *Store) Live(ctx context.Context, now time.Time, window time.Duration) (Live, error) {
	var l Live
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(monitors), 0), COALESCE(SUM(agents), 0)
		FROM instances WHERE last_seen >= ?`, now.Add(-window).Unix()).
		Scan(&l.Instances, &l.Monitors, &l.Agents)
	return l, err
}

// VersionCount is how many live hubs run a version.
type VersionCount struct {
	Version   string `json:"version"`
	Instances int    `json:"instances"`
}

// Versions lists the versions of the live hubs, most used first.
func (s *Store) Versions(ctx context.Context, now time.Time, window time.Duration) ([]VersionCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT version, COUNT(*) AS n FROM instances WHERE last_seen >= ?
		GROUP BY version ORDER BY n DESC, version DESC`, now.Add(-window).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionCount{}
	for rows.Next() {
		var v VersionCount
		if err := rows.Scan(&v.Version, &v.Instances); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Totals count hubs over longer spans.
type Totals struct {
	Seen30d int `json:"seen_30d"` // hubs that reported in the last 30 days
	New7d   int `json:"new_7d"`   // hubs whose first report is in the last 7 days
	Ever    int `json:"ever"`     // hubs kept (dropped after the retention)
}

func (s *Store) Totals(ctx context.Context, now time.Time) (Totals, error) {
	var t Totals
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(last_seen >= ?), 0),
			COALESCE(SUM(first_seen >= ?), 0),
			COUNT(*)
		FROM instances`,
		now.AddDate(0, 0, -30).Unix(), now.AddDate(0, 0, -7).Unix()).
		Scan(&t.Seen30d, &t.New7d, &t.Ever)
	return t, err
}

// Day is one row of the daily history.
type Day struct {
	Day string `json:"day"`
	Live
}

// RecordDay raises today's row to l where l is higher.
func (s *Store) RecordDay(ctx context.Context, now time.Time, l Live) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO daily (day, instances, monitors, agents) VALUES (?, ?, ?, ?)
		ON CONFLICT(day) DO UPDATE SET
			instances = MAX(instances, excluded.instances),
			monitors  = MAX(monitors, excluded.monitors),
			agents    = MAX(agents, excluded.agents)`,
		now.UTC().Format(time.DateOnly), l.Instances, l.Monitors, l.Agents)
	return err
}

// History returns the last days, oldest first.
func (s *Store) History(ctx context.Context, days int) ([]Day, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT day, instances, monitors, agents FROM
			(SELECT * FROM daily ORDER BY day DESC LIMIT ?)
		ORDER BY day`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Day{}
	for rows.Next() {
		var d Day
		if err := rows.Scan(&d.Day, &d.Instances, &d.Monitors, &d.Agents); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Prune drops hubs not seen since before.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE last_seen < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
