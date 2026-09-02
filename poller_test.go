package main

import "testing"

func TestCategory(t *testing.T) {
	cases := []struct {
		name string
		item prItem
		want string
	}{
		{
			name: "human author",
			item: prItem{Title: "bump the non-breaking group with 2 updates", User: struct {
				Login string `json:"login"`
			}{Login: "cujarrett"}},
			want: "human",
		},
		{
			name: "dependabot grouped npm bump",
			item: prItem{Title: "chore(deps): bump the non-breaking group in /spa with 3 updates", User: struct {
				Login string `json:"login"`
			}{Login: "dependabot[bot]"}},
			want: "dependabot-auto",
		},
		{
			name: "dependabot grouped actions bump",
			item: prItem{Title: "chore(deps): bump the actions group with 2 updates", User: struct {
				Login string `json:"login"`
			}{Login: "dependabot[bot]"}},
			want: "dependabot-auto",
		},
		{
			name: "dependabot major bump",
			item: prItem{Title: "chore(deps): bump typescript from 6.0.3 to 7.0.2", User: struct {
				Login string `json:"login"`
			}{Login: "dependabot[bot]"}},
			want: "dependabot-manual",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := category(c.item); got != c.want {
				t.Errorf("category() = %q, want %q", got, c.want)
			}
		})
	}
}
