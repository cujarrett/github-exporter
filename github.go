package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// GitHub's search API caps a page at 100 and the poller does not paginate,
// so this doubles as the point where counts start silently truncating.
const searchPageSize = 100

type githubClient struct {
	token      string
	org        string
	httpClient *http.Client
}

type searchResult struct {
	Items []prItem `json:"items"`
}

type prItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	User   struct {
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

// mergedSince returns PRs merged in repo after cutoff, oldest first is not
// guaranteed by the API - callers must sort if order matters.
func (c *githubClient) mergedSince(repo string, cutoff time.Time) ([]prItem, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr is:merged merged:>%s", c.org, repo, cutoff.UTC().Format(time.RFC3339))
	endpoint := fmt.Sprintf("https://api.github.com/search/issues?q=%s&sort=created&order=asc&per_page=%d",
		url.QueryEscape(q), searchPageSize)

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
		return nil, fmt.Errorf("github search returned %d for repo %s", resp.StatusCode, repo)
	}

	var result searchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Items, nil
}
