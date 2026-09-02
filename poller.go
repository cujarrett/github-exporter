package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var prMergedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "github_exporter_pr_merged_total",
		Help: "Merged pull requests, by repo and merge category.",
	},
	[]string{"repo", "category"},
)

// A failed poll and a quiet week both leave prMergedTotal flat, so an expired
// token is invisible without this.
var pollErrorsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "github_exporter_poll_errors_total",
		Help: "Failed GitHub polls, by repo.",
	},
	[]string{"repo"},
)

func init() {
	prometheus.MustRegister(prMergedTotal, pollErrorsTotal)
}

// category classifies a merged PR from title and author alone - the search
// API doesn't return the head branch, and dependabot's grouped-update titles
// ("bump the non-breaking group", "bump the actions group") are the only
// other signal that a PR was eligible for our auto-merge workflow.
func category(item prItem) string {
	if item.User.Login != "dependabot[bot]" {
		return "human"
	}
	title := strings.ToLower(item.Title)
	if strings.Contains(title, "bump the non-breaking group") || strings.Contains(title, "bump the actions group") {
		return "dependabot-auto"
	}
	return "dependabot-manual"
}

type poller struct {
	client  *githubClient
	repos   []string
	logger  *slog.Logger
	cursors map[string]time.Time
}

func newPoller(client *githubClient, repos []string, logger *slog.Logger) *poller {
	cursors := make(map[string]time.Time, len(repos))
	// Counting from process start rather than backfilling - a restart would
	// otherwise re-increment every PR inside the backfill window.
	start := time.Now()
	for _, r := range repos {
		cursors[r] = start
	}
	return &poller{client: client, repos: repos, logger: logger, cursors: cursors}
}

func (p *poller) run(ctx context.Context, interval time.Duration) {
	p.pollAll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollAll()
		}
	}
}

func (p *poller) pollAll() {
	for _, repo := range p.repos {
		p.pollRepo(repo)
	}
}

func (p *poller) pollRepo(repo string) {
	cutoff := p.cursors[repo]
	items, err := p.client.mergedSince(repo, cutoff)
	if err != nil {
		pollErrorsTotal.WithLabelValues(repo).Inc()
		p.logger.Error("poll failed", "repo", repo, "err", err)
		return
	}

	latest := cutoff
	for _, item := range items {
		if item.PullRequest.MergedAt == nil {
			continue
		}
		mergedAt := *item.PullRequest.MergedAt
		if !mergedAt.After(cutoff) {
			continue
		}
		prMergedTotal.WithLabelValues(repo, category(item)).Inc()
		if mergedAt.After(latest) {
			latest = mergedAt
		}
	}
	p.cursors[repo] = latest
}
