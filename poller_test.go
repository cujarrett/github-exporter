package main

import "testing"

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
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scope(c.item, c.facts); got != c.want {
				t.Errorf("scope() = %q, want %q", got, c.want)
			}
		})
	}
}
