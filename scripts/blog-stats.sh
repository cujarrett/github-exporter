#!/usr/bin/env bash
# Counts Renovate PRs merged since automerge went live and writes stats.json to the gist
# the blog's automerge post fetches.
set -euo pipefail

owner=cujarrett
# Midnight 2026-09-06 America/Chicago, when automerge went live.
since=2026-09-06T05:00:00Z
: "${GH_TOKEN:?}" "${GIST_TOKEN:?}" "${GIST_ID:?}"

# GitHub records whoever armed auto-merge as the merger, so autoMergeRequest is the signal.
query='query($q: String!, $endCursor: String) {
  search(query: $q, type: ISSUE, first: 100, after: $endCursor) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest { mergedAt autoMergeRequest { enabledAt } mergedBy { __typename } } }
  }
}'

# Search returns at most 1,000 results, so it runs once per 30-day window.
# Each window ends a second before the next starts, so no PR is counted twice.
windows=$(jq -rn --arg since "$since" '
  ($since | fromdateiso8601) as $start | (30 * 86400) as $step |
  range($start; now; $step) | "\(todate)..\(. + $step - 1 | todate)"')

search() {
  gh api graphql --paginate -f query="$query" -f q="$1" \
    --jq '.data.search.nodes[] | {merged_at: .mergedAt, renovate: (.autoMergeRequest != null or .mergedBy.__typename == "Bot")}'
}

prs=$(for window in $windows; do
  # exit fails the pipeline, so one failed window stops the run instead of undercounting.
  search "user:$owner is:pr is:merged author:app/renovate merged:$window" || exit
done | jq -s .)

# The 30 days before go-live, for the before-and-after chart on mattjarrett.dev.
# Dependabot still opened most dependency PRs then, and every one was merged by hand.
before=$(jq -rn --arg since "$since" '($since | fromdateiso8601) as $t | "\($t - 30 * 86400 | todate)..\($t - 1 | todate)"')
earlier=$(for author in app/renovate app/dependabot; do
  search "user:$owner is:pr is:merged author:$author merged:$before" || exit
done | jq -s .)

# One row per America/Chicago day. Stepping from noon keeps each date once across DST.
days=$(TZ=America/Chicago jq -c --arg from "${before%%..*}" --argjson earlier "$earlier" '
  def day: fromdateiso8601 | strflocaltime("%Y-%m-%d");
  (($earlier + .) | group_by(.merged_at | day)
    | map({key: (.[0].merged_at | day), value: {auto: ([.[] | select(.renovate)] | length), hand: ([.[] | select(.renovate | not)] | length)}})
    | from_entries) as $counts
  | [range(($from | fromdateiso8601) + 43200; now + 43200; 86400) | strflocaltime("%Y-%m-%d")]
  | map({date: ., auto: ($counts[.].auto // 0), hand: ($counts[.].hand // 0)})
' <<<"$prs")

stats=$(jq -c --argjson days "$days" '{
  since: "2026-09-06",
  renovate: [.[] | select(.renovate)] | length,
  me: [.[] | select(.renovate | not)] | length,
  updated: ([.[].merged_at] | max),
  days: $days
}' <<<"$prs")

# Each write adds a revision, and the post downloads the gist's revision list.
current=$(GH_TOKEN=$GIST_TOKEN gh api "gists/$GIST_ID" --jq '.files["stats.json"].content // ""')
if [[ $stats != "$current" ]]; then
  jq -n --arg stats "$stats" '{files: {"stats.json": {content: $stats}}}' |
    GH_TOKEN=$GIST_TOKEN gh api -X PATCH "gists/$GIST_ID" --input - >/dev/null
fi

echo "$stats"
