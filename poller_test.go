package main

import "testing"

func TestAuthor(t *testing.T) {
	cases := []struct {
		name  string
		login string
		want  string
	}{
		{name: "person", login: "cujarrett", want: "human"},
		{name: "dependabot", login: "dependabot[bot]", want: "dependabot"},
		{name: "another bot is not dependabot", login: "renovate[bot]", want: "human"},
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
