package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var version = "dev"

// The platform mounts secretsFrom Secrets as files. The env var stays as the
// fallback, which is how the token is set locally.
func readSecret(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	token := readSecret("/secrets/github-exporter-token/GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		logger.Error("GITHUB_TOKEN is required")
		os.Exit(1)
	}

	org := os.Getenv("GITHUB_ORG")
	if org == "" {
		org = "cujarrett"
	}

	// Empty means discover the account's own repos on every poll. Set REPOS only to
	// watch a subset.
	var repos []string
	if v := os.Getenv("REPOS"); v != "" {
		repos = strings.Split(v, ",")
		for i := range repos {
			repos[i] = strings.TrimSpace(repos[i])
		}
	}

	pollInterval := 5 * time.Minute
	if v := os.Getenv("POLL_INTERVAL_SECONDS"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil {
			logger.Error("invalid POLL_INTERVAL_SECONDS", "err", err)
			os.Exit(1)
		}
		pollInterval = time.Duration(secs) * time.Second
	}

	client := &githubClient{
		token:      token,
		org:        org,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		facts:      map[string]mergeFacts{},
		branches:   map[string]string{},
	}
	poller := newPoller(client, repos, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: ":" + port, Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go poller.run(ctx, pollInterval)

	go func() {
		logger.Info("server listening", "port", port, "version", version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	if err := srv.Shutdown(context.Background()); err != nil {
		logger.Error("shutdown error", "err", err)
	}
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","version":%q}`, version) //nolint:errcheck
}
