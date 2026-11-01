// Command pingtxt-telemetry receives the anonymous usage stats that pingtxt
// hubs send once an hour (when an admin opted in) and shows the totals.
//
//	pingtxt-telemetry              run the server (settings from env, see README)
//	pingtxt-telemetry healthcheck  exit 0 if the local server answers /healthz
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/federico-it/pingtxt-telemetry/internal/server"
	"github.com/federico-it/pingtxt-telemetry/internal/store"
)

var version = "dev"

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "":
		err = run()
	case "healthcheck":
		err = healthcheck()
	case "version", "-version", "--version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (use healthcheck or version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pingtxt-telemetry:", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func run() error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("TELEMETRY_LOG_LEVEL", "info"))); err != nil {
		return errors.New("TELEMETRY_LOG_LEVEL: use debug, info, warn or error")
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	window, err := time.ParseDuration(env("TELEMETRY_LIVE_WINDOW", "2h"))
	if err != nil || window < 10*time.Minute {
		return errors.New("TELEMETRY_LIVE_WINDOW: a duration of at least 10m, like 2h")
	}
	keepDays, err := strconv.Atoi(env("TELEMETRY_KEEP_DAYS", "180"))
	if err != nil || keepDays < 1 {
		return errors.New("TELEMETRY_KEEP_DAYS: a number of days, at least 1")
	}
	ipRate, err := strconv.Atoi(env("TELEMETRY_IP_RATE", "30"))
	if err != nil {
		return errors.New("TELEMETRY_IP_RATE: reports per IP per minute (0 = no limit)")
	}

	dir := env("TELEMETRY_DATA", "/data")
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer st.Close()

	s := server.New(server.Config{
		Window:         window,
		TrustProxy:     env("TELEMETRY_TRUST_PROXY", "false") == "true",
		ClientIPHeader: env("TELEMETRY_CLIENT_IP_HEADER", ""),
		IPRate:         ipRate,
	}, st, log, version)

	addr := env("TELEMETRY_ADDR", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	keep := time.Duration(keepDays) * 24 * time.Hour
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			if err := s.Tick(ctx, keep); err != nil && ctx.Err() == nil {
				log.Error("tick", "err", err)
			}
			s.Sweep()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "version", version, "data", dir, "live_window", window.String())
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthcheck is for the Docker HEALTHCHECK: the image has no shell or curl.
func healthcheck() error {
	host, port, err := net.SplitHostPort(env("TELEMETRY_ADDR", ":8080"))
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: http %d", resp.StatusCode)
	}
	return nil
}
