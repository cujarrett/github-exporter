package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Every combination is written on every poll, so one that drops to zero reports
// zero instead of leaving a stale series behind.
var authors = []string{"human", "dependabot"}
var merges = []string{"clicked", "auto", "bot"}

// Written on every poll for the same reason as categories - a conclusion that
// stops happening reports zero instead of holding its last value forever.
var conclusions = []string{"success", "failure", "cancelled", "skipped"}

// The windows the dashboard graphs. Each poll fetches once out to the widest
// and buckets locally, so it's one search call per repo, not one per window.
var windows = []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour}

var prMerged = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "github_exporter_pr_merged",
		Help: "Pull requests merged within a window, by who opened it and how it reached main.",
	},
	[]string{"repo", "author", "merge", "window"},
)

var prOpened = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "github_exporter_pr_opened",
		Help: "Pull requests opened within a window, regardless of current state, by who opened it.",
	},
	[]string{"repo", "author", "window"},
)

// Point-in-time, not windowed - "how many PRs are open right now".
var prOpen = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "github_exporter_pr_open",
		Help: "Pull requests currently open, by who opened it.",
	},
	[]string{"repo", "author"},
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
	prometheus.MustRegister(prMerged, prOpened, prOpen, pollErrorsTotal, workflowRuns)
}

// author says who opened the PR. Everything about how it merged comes from
// mergeFacts instead, because a title cannot tell you that.
func author(item prItem) string {
	if item.User.Login == "dependabot[bot]" {
		return "dependabot"
	}
	return "human"
}

// mergeKind reads GitHub's own record rather than inferring from a title.
// "auto" is a merge GitHub performed once checks passed, "clicked" is a person
// pressing the button, "bot" is a workflow or app merging with its own token.
func mergeKind(f mergeFacts) string {
	switch {
	case f.autoMerge:
		return "auto"
	case strings.HasSuffix(f.mergedBy, "[bot]"):
		return "bot"
	default:
		return "clicked"
	}
}

type poller struct {
	client *githubClient
	repos  []string
	logger *slog.Logger
}

func newPoller(client *githubClient, repos []string, logger *slog.Logger) *poller {
	return &poller{client: client, repos: repos, logger: logger}
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

// pollRepo recounts every window from scratch rather than tracking a cursor, so
// a restart reports the same numbers the previous process did.
func (p *poller) pollRepo(repo string) {
	now := time.Now()
	widest := windows[len(windows)-1]

	p.pollMerged(repo, now, widest)
	p.pollOpened(repo, now, widest)
	p.pollOpenNow(repo)
	p.pollWorkflows(repo, now.Add(-windows[2])) // 30d, the one figure the CI panel headlines
}

type authorMergeKey struct{ author, merge string }

func (p *poller) pollMerged(repo string, now time.Time, widest time.Duration) {
	items, err := p.client.mergedSince(repo, now.Add(-widest))
	if err != nil {
		pollErrorsTotal.WithLabelValues(repo).Inc()
		p.logger.Error("poll failed", "repo", repo, "err", err)
		return
	}
	if len(items) == searchPageSize {
		p.logger.Warn("hit search page limit, merged counts may be low", "repo", repo, "limit", searchPageSize)
	}

	// Every window is a subset of the widest one, so one search feeds all of them.
	counts := make(map[time.Duration]map[authorMergeKey]float64, len(windows))
	for _, w := range windows {
		counts[w] = map[authorMergeKey]float64{}
	}
	for _, item := range items {
		if item.PullRequest.MergedAt == nil {
			continue
		}
		facts, err := p.client.mergeFactsFor(repo, item.Number)
		if err != nil {
			pollErrorsTotal.WithLabelValues(repo).Inc()
			p.logger.Error("merge facts failed", "repo", repo, "pr", item.Number, "err", err)
			return
		}
		k := authorMergeKey{author(item), mergeKind(facts)}
		for _, w := range windows {
			if item.PullRequest.MergedAt.After(now.Add(-w)) {
				counts[w][k]++
			}
		}
	}

	for _, w := range windows {
		label := windowLabel(w)
		for _, a := range authors {
			for _, m := range merges {
				prMerged.WithLabelValues(repo, a, m, label).Set(counts[w][authorMergeKey{a, m}])
			}
		}
	}
}

func (p *poller) pollOpened(repo string, now time.Time, widest time.Duration) {
	items, err := p.client.openedSince(repo, now.Add(-widest))
	if err != nil {
		pollErrorsTotal.WithLabelValues(repo).Inc()
		p.logger.Error("opened poll failed", "repo", repo, "err", err)
		return
	}
	if len(items) == searchPageSize {
		p.logger.Warn("hit search page limit, opened counts may be low", "repo", repo, "limit", searchPageSize)
	}

	counts := make(map[time.Duration]map[string]float64, len(windows))
	for _, w := range windows {
		counts[w] = map[string]float64{}
	}
	for _, item := range items {
		a := author(item)
		for _, w := range windows {
			if item.CreatedAt.After(now.Add(-w)) {
				counts[w][a]++
			}
		}
	}

	for _, w := range windows {
		label := windowLabel(w)
		for _, a := range authors {
			prOpened.WithLabelValues(repo, a, label).Set(counts[w][a])
		}
	}
}

func (p *poller) pollOpenNow(repo string) {
	items, err := p.client.openNow(repo)
	if err != nil {
		pollErrorsTotal.WithLabelValues(repo).Inc()
		p.logger.Error("open poll failed", "repo", repo, "err", err)
		return
	}
	if len(items) == searchPageSize {
		p.logger.Warn("hit search page limit, open count may be low", "repo", repo, "limit", searchPageSize)
	}

	counts := map[string]float64{}
	for _, item := range items {
		counts[author(item)]++
	}
	for _, a := range authors {
		prOpen.WithLabelValues(repo, a).Set(counts[a])
	}
}

// windowLabel renders a duration as whole days, which is the only granularity
// any window is ever set to.
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
