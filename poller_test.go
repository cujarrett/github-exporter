package main

import (
	"encoding/json"
	"testing"
)

func TestErroredUpdates(t *testing.T) {
	body := "## Errored\n\n" +
		" - [ ] <!-- retry-branch=renovate/pin-dependencies -->chore(deps): pin dependencies\n\n" +
		"## Pending Status Checks\n\n" +
		" - [ ] <!-- unpend-branch=renovate/argo-cd-10.x -->chore(deps): update helm release argo-cd\n\n" +
		"## Open\n\n" +
		" - [ ] <!-- rebase-branch=renovate/mariadb-13.x -->chore(deps): update mariadb\n"
	if got := erroredUpdates(body); got != 1 {
		t.Errorf("erroredUpdates() = %d, want 1", got)
	}
}

func TestPendingUpdates(t *testing.T) {
	body := "## Pending Status Checks\n\n" +
		" - [ ] <!-- unpend-branch=renovate/loki-7.x -->chore(deps): update helm release loki to v7.5.0\n" +
		" - [ ] <!-- unpend-branch=renovate/redis-8.x -->chore(deps): update redis docker tag to v8\n\n" +
		"## Open\n\n" +
		" - [ ] <!-- rebase-branch=renovate/mariadb-13.x -->chore(deps): update mariadb\n"
	got := pendingUpdates(body)
	want := []string{"update helm release loki to v7.5.0", "update redis docker tag to v8"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pendingUpdates() = %q, want %q", got, want)
	}
}

func TestAuthor(t *testing.T) {
	cases := []struct {
		name  string
		login string
		want  string
	}{
		{name: "person", login: "cujarrett", want: "human"},
		{name: "dependabot", login: "dependabot[bot]", want: "bot"},
		{name: "renovate is a bot too", login: "renovate[bot]", want: "bot"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			item := prItem{User: struct {
				Login string `json:"login"`
			}{Login: c.login}}
			if got := author(item); got != c.want {
				t.Errorf("author() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMergeKind(t *testing.T) {
	cases := []struct {
		name  string
		facts mergeFacts
		want  string
	}{
		// auto wins over the merger, because GitHub records whoever armed
		// auto-merge as the merger rather than a bot.
		{name: "armed auto-merge", facts: mergeFacts{mergedBy: "cujarrett", autoMerge: true}, want: "auto"},
		{name: "person clicked merge", facts: mergeFacts{mergedBy: "cujarrett"}, want: "clicked"},
		{name: "workflow merged", facts: mergeFacts{mergedBy: "github-actions[bot]"}, want: "bot"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergeKind(c.facts); got != c.want {
				t.Errorf("mergeKind() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestScope(t *testing.T) {
	dependabot := prItem{User: struct {
		Login string `json:"login"`
	}{Login: "dependabot[bot]"}}
	human := prItem{User: struct {
		Login string `json:"login"`
	}{Login: "cujarrett"}}

	cases := []struct {
		name  string
		item  prItem
		facts mergeFacts
		want  string
	}{
		{name: "human PR is never in scope", item: human, facts: mergeFacts{branch: "some-feature"}, want: "human"},
		{name: "grouped minor/patch bump", item: dependabot, facts: mergeFacts{branch: "dependabot/go_modules/api/non-breaking-abc123"}, want: "auto-candidate"},
		{name: "grouped actions bump", item: dependabot, facts: mergeFacts{branch: "dependabot/github_actions/actions-abc123"}, want: "auto-candidate"},
		{name: "ungrouped major bump", item: dependabot, facts: mergeFacts{branch: "dependabot/go_modules/api/go-1.27.0"}, want: "excluded"},
		{name: "renovate grouped bump", item: dependabot, facts: mergeFacts{branch: "renovate/non-breaking"}, want: "auto-candidate"},
		{name: "renovate major bump", item: dependabot, facts: mergeFacts{branch: "renovate/typescript-7.x"}, want: "excluded"},
		{name: "ungrouped bump the bot labelled for automerge", item: dependabot, facts: mergeFacts{branch: "renovate/argo-cd-10.x", labels: []string{"automerge"}}, want: "auto-candidate"},
		{name: "labelled human PR stays human", item: human, facts: mergeFacts{branch: "some-feature", labels: []string{"automerge"}}, want: "human"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scope(c.item, c.facts); got != c.want {
				t.Errorf("scope() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRequiredChecks(t *testing.T) {
	protected := func(contexts []string) branchResponse {
		var b branchResponse
		if err := json.Unmarshal([]byte(`{"protected":true,"protection":{"required_status_checks":{"contexts":[]}}}`), &b); err != nil {
			t.Fatal(err)
		}
		b.Protection.RequiredStatusChecks.Contexts = contexts
		return b
	}
	cases := []struct {
		name string
		b    branchResponse
		want *int
	}{
		{name: "unprotected gates nothing", b: branchResponse{}, want: ptr(0)},
		{name: "protection hidden from the token", b: branchResponse{Protected: true}, want: nil},
		{name: "protected with no checks", b: protected(nil), want: ptr(0)},
		{name: "protected with one check", b: protected([]string{"test"}), want: ptr(1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requiredChecks(c.b)
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Errorf("requiredChecks() = %v, want %v", got, c.want)
			}
		})
	}
}

func ptr(n int) *int { return &n }
