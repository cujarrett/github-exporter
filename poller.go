package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Every category is written on every poll, so a category that drops to zero
// reports zero instead of leaving a stale series behind.
var categories = []string{"human", "dependabot-auto", "dependabot-manual"}

// Written on every poll for the same reason as categories - a conclusion that
// stops happening reports zero instead of holding its last value forever.
var conclusions = []string{"success", "failure", "cancelled", "skipped"}

// The short window is what a trend line reads from; the long one is the headline
// count. Both are recounted each poll, so neither needs a cursor.
const shortWindow = 24 * time.Hour

var prMerged = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "github_exporter_pr_merged",
		Help: "Pull requests merged within the lookback window, by repo and merge category.",
	},
	[]string{"repo", "category", "window"},
)

// Workflow runs on the default branch, so a pass rate reads as "did main stay
// green" rather than counting every push to every feature branch.
var workflowRuns = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "github_exporter_workflow_runs",
		Help: "Workflow runs on the default branch within the lookback window, by repo and conclusion.",
	},
	[]string{"repo", "conclusion"},
)

// A failed poll and a quiet week both leave prMerged flat, so an expired token
// is invisible without this.
var pollErrorsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "github_exporter_poll_errors_total",
		Help: "Failed GitHub polls, by repo.",
	},
	[]string{"repo"},
)

func init() {
	prometheus.MustRegister(prMerged, pollErrorsTotal, workflowRuns)
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
	client   *githubClient
	repos    []string
	lookback time.Duration
	logger   *slog.Logger
}

func newPoller(client *githubClient, repos []string, lookback time.Duration, logger *slog.Logger) *poller {
	return &poller{client: client, repos: repos, lookback: lookback, logger: logger}
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
	repos := p.repos
	if len(repos) == 0 {
		discovered, err := p.client.listRepos()
		if err != nil {
			p.logger.Error("repo discovery failed, skipping this poll", "err", err)
			return
		}
		repos = discovered
	}
	for _, repo := range repos {
		p.pollRepo(repo)
	}
}

// pollRepo recounts the whole window rather than tracking a cursor, so a
// restart reports the same numbers the previous process did.
func (p *poller) pollRepo(repo string) {
	now := time.Now()
	items, err := p.client.mergedSince(repo, now.Add(-p.lookback))
	if err != nil {
		pollErrorsTotal.WithLabelValues(repo).Inc()
		p.logger.Error("poll failed", "repo", repo, "err", err)
		return
	}

	if len(items) == searchPageSize {
		p.logger.Warn("hit search page limit, counts may be low", "repo", repo, "limit", searchPageSize)
	}

	// The short window is a subset of the long one, so one search feeds both.
	long := make(map[string]float64, len(categories))
	short := make(map[string]float64, len(categories))
	shortCutoff := now.Add(-shortWindow)
	for _, item := range items {
		if item.PullRequest.MergedAt == nil {
			continue
		}
		c := category(item)
		long[c]++
		if item.PullRequest.MergedAt.After(shortCutoff) {
			short[c]++
		}
	}

	longLabel := windowLabel(p.lookback)
	for _, c := range categories {
		prMerged.WithLabelValues(repo, c, longLabel).Set(long[c])
		prMerged.WithLabelValues(repo, c, windowLabel(shortWindow)).Set(short[c])
	}

	p.pollWorkflows(repo, now.Add(-p.lookback))
}

// windowLabel renders a duration as whole days, which is the only granularity
// either window is ever set to.
func windowLabel(d time.Duration) string {
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (p *poller) pollWorkflows(repo string, cutoff time.Time) {
	runs, err := p.client.workflowRunsSince(repo, cutoff)
	if err != nil {
		pollErrorsTotal.WithLabelValues(repo).Inc()
		p.logger.Error("workflow poll failed", "repo", repo, "err", err)
		return
	}

	counts := make(map[string]float64, len(conclusions))
	for _, r := range runs {
		counts[r.Conclusion]++
	}
	for _, c := range conclusions {
		workflowRuns.WithLabelValues(repo, c).Set(counts[c])
	}
}
