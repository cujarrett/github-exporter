package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

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

// mergedSince returns PRs merged in repo after cutoff, oldest first is not
// guaranteed by the API - callers must sort if order matters.
func (c *githubClient) mergedSince(repo string, cutoff time.Time) ([]prItem, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr is:merged merged:>%s", c.org, repo, cutoff.UTC().Format(time.RFC3339))
	endpoint := "https://api.github.com/search/issues?q=" + url.QueryEscape(q) + "&sort=created&order=asc&per_page=100"

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
