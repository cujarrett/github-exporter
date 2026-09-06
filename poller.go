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
var authors = []string{"human", "bot"}
var merges = []string{"clicked", "auto", "bot"}

// scope is what tells the auto-merge candidate pile apart from the pile that
// never had a chance: a human PR is never in scope for any bot's auto-merge
// workflow, and a bot PR outside the grouped branches (a major bump) isn't
// either.
var scopes = []string{"human", "auto-candidate", "excluded"}

// Written on every poll for the same reason as categories - a conclusion that
// stops happening reports zero instead of holding its last value forever.
var conclusions = []string{"success", "failure", "cancelled", "skipped"}

// The windows the dashboard graphs. Each poll fetches once out to the widest
// and buckets locally, so it's one search call per repo, not one per window.
var windows = []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour}

var prMerged = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "github_exporter_pr_merged",
		Help: "Pull requests merged within a window, by who opened it, how it reached main, and whether it was ever eligible for the auto-merge workflow.",
	},
	[]string{"repo", "author", "merge", "scope", "window"},
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
		Help: "Pull requests currently open, by who opened it and whether it's eligible for auto-merge.",
	},
	[]string{"repo", "author", "scope"},
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
var prOpenBlocked = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "github_exporter_pr_open_blocked",
		Help: "Pull requests with auto-merge armed that GitHub is refusing to merge, almost always a failed required check. Automation is on and a person is still needed.",
	},
	[]string{"repo"},
)

var pollErrorsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "github_exporter_poll_errors_total",
		Help: "Failed GitHub polls, by repo.",
	},
	[]string{"repo"},
)

func init() {
	prometheus.MustRegister(prMerged, prOpened, prOpen, prOpenBlocked, pollErrorsTotal, workflowRuns)
}

// author says who opened the PR. Any GitHub App account - Dependabot, Renovate,
// or otherwise - uses the "[bot]" suffix, so this isn't tied to one tool's name.
// Everything about how it merged comes from mergeFacts instead, because a
// title cannot tell you that.
func author(item prItem) string {
	if strings.HasSuffix(item.User.Login, "[bot]") {
		return "bot"
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

// candidateScope classifies whether a PR could ever have been auto-merged, merged
// or not. Both bots name a grouped minor/patch branch after the group, so
// Dependabot's non-breaking-<hash>/actions-<hash> and Renovate's bare
// renovate/non-breaking both match. A major always gets its own branch name.
func candidateScope(isBot bool, branch string) string {
	if !isBot {
		return "human"
	}
	if strings.Contains(branch, "/non-breaking") || strings.Contains(branch, "/actions-") {
		return "auto-candidate"
	}
	return "excluded"
}

func scope(item prItem, f mergeFacts) string {
	return candidateScope(author(item) == "bot", f.branch)
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

type mergedKey struct{ author, merge, scope string }

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
	counts := make(map[time.Duration]map[mergedKey]float64, len(windows))
	for _, w := range windows {
		counts[w] = map[mergedKey]float64{}
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
		k := mergedKey{author(item), mergeKind(facts), scope(item, facts)}
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
				for _, s := range scopes {
					prMerged.WithLabelValues(repo, a, m, s, label).Set(counts[w][mergedKey{a, m, s}])
				}
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

	type authorScopeKey struct{ author, scope string }
	counts := map[authorScopeKey]float64{}
	blocked := 0.0
	for _, item := range items {
		a := author(item)
		branch := ""
		if a == "bot" {
			facts, err := p.client.openFactsFor(repo, item.Number)
			if err != nil {
				pollErrorsTotal.WithLabelValues(repo).Inc()
				p.logger.Error("open PR lookup failed", "repo", repo, "pr", item.Number, "err", err)
				continue
			}
			branch = facts.branch
			if facts.autoMerge && facts.blocked {
				blocked++
			}
		}
		counts[authorScopeKey{a, candidateScope(a == "bot", branch)}]++
	}
	prOpenBlocked.WithLabelValues(repo).Set(blocked)
	for _, a := range authors {
		for _, s := range scopes {
			prOpen.WithLabelValues(repo, a, s).Set(counts[authorScopeKey{a, s}])
		}
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
