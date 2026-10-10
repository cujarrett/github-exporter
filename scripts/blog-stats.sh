#!/usr/bin/env bash
# Counts Renovate PRs merged since automerge went live and publishes them to a gist.
# - stats.json: the count the blog's automerge post fetches
# - prs.json: how each PR merged, cached because a merged PR never changes
set -euo pipefail

owner=cujarrett
# Midnight 2026-09-06 America/Chicago, when automerge went live.
since=2026-09-06T05:00:00Z
: "${GH_TOKEN:?}" "${GIST_TOKEN:?}" "${GIST_ID:?}"

cache=$(GH_TOKEN=$GIST_TOKEN gh api "gists/$GIST_ID" --jq '.files["prs.json"].content // "{}"')
seen='{}'

keys=$(gh api --paginate -X GET search/issues \
  -f q="user:$owner is:pr is:merged author:app/renovate merged:>=$since" -f per_page=100 \
  --jq '.items[] | "\(.repository_url | split("/") | last)#\(.number)"')

for key in $keys; do
  pr=$(jq -c --arg k "$key" '.[$k] // empty' <<<"$cache")
  if [[ -z $pr ]]; then
    # GitHub records whoever armed auto-merge as the merger, so auto_merge is the signal.
    pr=$(gh api "repos/$owner/${key%#*}/pulls/${key#*#}" \
      --jq '{merged_at, renovate: (.auto_merge != null or (.merged_by.login | endswith("[bot]")))}')
  fi
  seen=$(jq -c --arg k "$key" --argjson pr "$pr" '. + {($k): $pr}' <<<"$seen")
done

stats=$(jq -c '{
  since: "2026-09-06",
  renovate: [.[] | select(.renovate)] | length,
  me: [.[] | select(.renovate | not)] | length,
  updated: ([.[].merged_at] | max)
}' <<<"$seen")

jq -n --arg stats "$stats" --arg prs "$(jq -S . <<<"$seen")" \
  '{files: {"stats.json": {content: $stats}, "prs.json": {content: $prs}}}' |
  GH_TOKEN=$GIST_TOKEN gh api -X PATCH "gists/$GIST_ID" --input - >/dev/null

echo "$stats"
