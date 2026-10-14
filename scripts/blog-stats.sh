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

prs=$(for window in $windows; do
  # exit fails the pipeline, so one failed window stops the run instead of undercounting.
  gh api graphql --paginate -f query="$query" \
    -f q="user:$owner is:pr is:merged author:app/renovate merged:$window" \
    --jq '.data.search.nodes[] | {merged_at: .mergedAt, renovate: (.autoMergeRequest != null or .mergedBy.__typename == "Bot")}' || exit
done | jq -s .)

stats=$(jq -c '{
  since: "2026-09-06",
  renovate: [.[] | select(.renovate)] | length,
  me: [.[] | select(.renovate | not)] | length,
  updated: ([.[].merged_at] | max)
}' <<<"$prs")

# Each write adds a revision, and the post downloads the gist's revision list.
current=$(GH_TOKEN=$GIST_TOKEN gh api "gists/$GIST_ID" --jq '.files["stats.json"].content // ""')
if [[ $stats != "$current" ]]; then
  jq -n --arg stats "$stats" '{files: {"stats.json": {content: $stats}}}' |
    GH_TOKEN=$GIST_TOKEN gh api -X PATCH "gists/$GIST_ID" --input - >/dev/null
fi

echo "$stats"
