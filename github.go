package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// GitHub's search API caps a page at 100 and the poller does not paginate,
// so this doubles as the point where counts start silently truncating.
const searchPageSize = 100

// GitHub's Search API has its own, much tighter limit than the core API - 30
// requests/minute for an authenticated user, enforced separately from the
// 5000/hour budget everything else here shares. Three searches per repo
// (merged, opened, open) across ~20 repos blows through that in under 20
// seconds without this, and every repo after that 403s for the rest of the poll.
const minSearchInterval = 2200 * time.Millisecond

// A throttled search clears on its own, so retrying beats losing the repo's
// counts until the next poll. Four attempts covers ~15s of backoff.
const (
	searchAttempts  = 4
	searchRetryBase = 2 * time.Second
)

type githubClient struct {
	token      string
	org        string
	httpClient *http.Client

	factsMu sync.RWMutex
	facts   map[string]mergeFacts


	searchMu   sync.Mutex
	lastSearch time.Time
}

type searchResult struct {
	Items []prItem `json:"items"`
}

type prItem struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	PullRequest struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
}

type repoItem struct {
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
	Fork     bool   `json:"fork"`
}

// listRepos returns the account's own active repo names. Called on every poll, so
// a new repo starts reporting without anyone editing config.
func (c *githubClient) listRepos() ([]string, error) {
	var names []string
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("https://api.github.com/users/%s/repos?type=owner&per_page=100&page=%d", c.org, page)
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+c.token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		var batch []repoItem
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close() //nolint:errcheck
			return nil, fmt.Errorf("github repo list returned %d for %s", resp.StatusCode, c.org)
		}
		err = json.NewDecoder(resp.Body).Decode(&batch)
		resp.Body.Close() //nolint:errcheck
		if err != nil {
			return nil, err
		}
		for _, r := range batch {
			if !r.Archived && !r.Fork {
				names = append(names, r.Name)
			}
		}
		if len(batch) < 100 {
			return names, nil
		}
	}
}

// mergeFacts is how a merged PR actually reached main. Cached forever by repo and
// number, because none of it can change once the PR is merged - without the cache
// this would be two API calls per PR on every poll.
type mergeFacts struct {
	mergedBy  string
	autoMerge bool
	branch    string
}

func (c *githubClient) mergeFactsFor(repo string, number int) (mergeFacts, error) {
	key := fmt.Sprintf("%s#%d", repo, number)
	c.factsMu.RLock()
	f, ok := c.facts[key]
	c.factsMu.RUnlock()
	if ok {
		return f, nil
	}

	var pr struct {
		MergedBy struct {
			Login string `json:"login"`
		} `json:"merged_by"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		AutoMerge *struct{} `json:"auto_merge"`
	}
	if err := c.getJSON(fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", c.org, repo, number), &pr); err != nil {
		return mergeFacts{}, err
	}
	// GitHub keeps auto_merge populated after the merge and names whoever armed
	// it as the merger, so merged_by alone cannot tell an armed merge from a
	// clicked one. Renovate arms its own PRs, so this is the only signal that
	// separates them from a workflow merging with its own token.
	f = mergeFacts{
		mergedBy:  pr.MergedBy.Login,
		branch:    pr.Head.Ref,
		autoMerge: pr.AutoMerge != nil,
	}

	c.factsMu.Lock()
	c.facts[key] = f
	c.factsMu.Unlock()
	return f, nil
}

// openFacts is what an open PR looks like right now. Deliberately not cached:
// caching an open PR would freeze its state as "not merged, not blocked"
// forever, even after that stops being true.
type openFacts struct {
	branch    string
	autoMerge bool
	blocked   bool
}

// openFactsFor reads the head branch so a PR can be scope-classified, plus
// whether auto-merge is armed and GitHub is refusing to merge it anyway.
// mergeable_state is "blocked" when a required check has failed or is missing,
// which is the one state where automation is armed and still costing a person
// their attention.
func (c *githubClient) openFactsFor(repo string, number int) (openFacts, error) {
	var pr struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		AutoMerge      *struct{} `json:"auto_merge"`
		MergeableState string    `json:"mergeable_state"`
	}
	if err := c.getJSON(fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", c.org, repo, number), &pr); err != nil {
		return openFacts{}, err
	}
	return openFacts{
		branch:    pr.Head.Ref,
		autoMerge: pr.AutoMerge != nil,
		blocked:   pr.MergeableState == "blocked",
	}, nil
}

func (c *githubClient) getJSON(endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github returned %d for %s", resp.StatusCode, endpoint)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type workflowRun struct {
	Conclusion string `json:"conclusion"`
}

type workflowRunsResult struct {
	Runs []workflowRun `json:"workflow_runs"`
}

// workflowRunsSince returns default-branch workflow runs created after cutoff.
// Runs still in flight carry an empty conclusion and are dropped, so a poll
// mid-build does not report a phantom outcome.
func (c *githubClient) workflowRunsSince(repo string, cutoff time.Time) ([]workflowRun, error) {
	endpoint := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/actions/runs?branch=main&created=%s&per_page=%d",
		c.org, repo, url.QueryEscape(">"+cutoff.UTC().Format("2006-01-02")), searchPageSize)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github workflow runs returned %d for repo %s", resp.StatusCode, repo)
	}

	var result workflowRunsResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	out := make([]workflowRun, 0, len(result.Runs))
	for _, r := range result.Runs {
		if r.Conclusion != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// throttleSearch blocks until minSearchInterval has passed since the last
// search call, serializing every caller so the whole poll - not just one
// goroutine - stays under GitHub's per-minute Search API limit.
func (c *githubClient) throttleSearch() {
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if wait := minSearchInterval - time.Since(c.lastSearch); wait > 0 {
		time.Sleep(wait)
	}
	c.lastSearch = time.Now()
}

// searchPRs runs a GitHub search/issues query and returns the matching items.
// mergedSince, openedSince and openNow all share this - only the query string
// differs between them.
func (c *githubClient) searchPRs(q string) ([]prItem, error) {
	endpoint := fmt.Sprintf("https://api.github.com/search/issues?q=%s&sort=created&order=asc&per_page=%d",
		url.QueryEscape(q), searchPageSize)

	var lastStatus int
	for attempt := range searchAttempts {
		c.throttleSearch()

		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+c.token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			var result searchResult
			err := json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close() //nolint:errcheck
			if err != nil {
				return nil, err
			}
			return result.Items, nil
		}

		lastStatus = resp.StatusCode
		wait := retryAfter(resp, attempt)
		resp.Body.Close() //nolint:errcheck

		// A 403 here is the search rate limit, not a permission problem, and it
		// clears on its own. Anything else is not going to improve by waiting.
		if lastStatus != http.StatusForbidden && lastStatus != http.StatusTooManyRequests {
			break
		}
		time.Sleep(wait)
	}

	return nil, fmt.Errorf("github search returned %d for query %q", lastStatus, q)
}

// retryAfter honours GitHub's own backoff headers when it sends them and falls
// back to doubling, so a throttled poll waits seconds rather than losing the
// window until the next tick.
func retryAfter(resp *http.Response, attempt int) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Until(time.Unix(unix, 0)); d > 0 && d < time.Minute {
				return d
			}
		}
	}
	return time.Duration(1<<attempt) * searchRetryBase
}

// mergedSince returns PRs merged in repo after cutoff, oldest first is not
// guaranteed by the API - callers must sort if order matters.
func (c *githubClient) mergedSince(repo string, cutoff time.Time) ([]prItem, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr is:merged merged:>%s", c.org, repo, cutoff.UTC().Format(time.RFC3339))
	return c.searchPRs(q)
}

// openedSince returns PRs opened in repo after cutoff, regardless of current state.
func (c *githubClient) openedSince(repo string, cutoff time.Time) ([]prItem, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr created:>%s", c.org, repo, cutoff.UTC().Format(time.RFC3339))
	return c.searchPRs(q)
}

// openNow returns PRs currently open in repo - a point-in-time snapshot, not a window.
func (c *githubClient) openNow(repo string) ([]prItem, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr is:open", c.org, repo)
	return c.searchPRs(q)
}
