# github-exporter

Polls the GitHub API for pull request activity across every repo the account owns and exposes Prometheus gauges split by repo, who opened the PR, how it merged, and whether it was ever eligible for an auto-merge workflow - so Grafana can compare auto-merged bot volume against everything a person still has to review.

## Commands

| Command | What it does |
|---|---|
| `just ci` | Lint + test + build (run before pushing) |
| `just run` | Start the server locally on port 8080 |
| `just test` | Run tests with race detector |
| `just lint` | go mod tidy -diff + golangci-lint |

## Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/healthz` | Liveness probe |
| `GET` | `/metrics` | Prometheus metrics |

## Metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `github_exporter_pr_merged` | gauge | `repo`, `author`, `merge`, `scope`, `window` | Merged PRs inside a window, recounted every poll. `author` is `human` or `bot` (any GitHub App account - Dependabot, Renovate, or otherwise). `merge` is how it reached main, read from GitHub's own record rather than the title: `auto` (GitHub merged it once checks passed), `clicked` (a person pressed the button), `bot` (a workflow merged with its own token). `scope` is whether the PR was ever eligible for an auto-merge workflow: `human` (a person's own PR, never in scope), `auto-candidate` (a grouped minor/patch or actions bump), `excluded` (an ungrouped major bump - correctly required a human). The eligibility check understands both Dependabot's and Renovate's grouping conventions. `window` is `1d`, `7d`, `30d` or `365d` |
| `github_exporter_pr_opened` | gauge | `repo`, `author`, `window` | PRs opened inside a window, regardless of current state. Same `window` values as above |
| `github_exporter_pr_open` | gauge | `repo`, `author`, `scope` | PRs open right now - a snapshot, not a window. Same `scope` values as above |
| `github_exporter_workflow_runs` | gauge | `repo`, `conclusion` | Workflow runs on the default branch in the last 30 days. `conclusion` is `success`, `failure`, `cancelled` or `skipped`. Runs still in flight carry no conclusion and are not counted |
| `github_exporter_repo_allow_auto_merge` | gauge | `repo` | 1 when the repo's "Allow auto-merge" setting is on. Off means Renovate's armed merges never fire and it merges on its own next visit instead, hours later. Read hourly |
| `github_exporter_repo_required_checks` | gauge | `repo` | Status checks the default branch requires before a merge. Zero means auto-merge has nothing to wait on. Read hourly |
| `github_exporter_repo_vulnerability_alerts` | gauge | `repo` | 1 when Dependabot alerts are on. Off means Renovate never learns a dependency is vulnerable, so its fix waits out the normal release age. Read hourly |
| `github_exporter_repo_settings_visible` | gauge | `repo` | 1 when the token can read every setting above. Zero means those gauges are missing for the repo, and alerts built on them cannot fire. Read hourly |
| `github_exporter_renovate_errored` | gauge | `repo` | Updates on the Renovate Dependency Dashboard that failed to build a branch. These have no PR, so nothing else shows them. Absent when the repo has no dashboard. Read hourly |
| `github_exporter_poll_errors_total` | counter | `repo` | Failed GitHub polls. Alert on this - a dead token otherwise looks like a quiet week |

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_TOKEN` | in local dev | | Token with read access to the tracked repos. In the cluster it arrives as a mounted file instead |
| `GITHUB_TOKEN_FILE` | no | `/secrets/github-exporter-token/GITHUB_TOKEN` | Path to the mounted token |
| `REPOS` | no | | Comma-separated repo names. Omit to poll every active repo the account owns, rediscovered on each poll |
| `GITHUB_ORG` | no | `cujarrett` | GitHub org/user the repos belong to |
| `POLL_INTERVAL_SECONDS` | no | `300` | How often to query the GitHub search API |
| `PORT` | no | `8080` | HTTP listen port |

## Deployment

ARM64 image built by CI, pushed to GHCR and deployed to the homelab cluster by ArgoCD.

The token is a fine-grained PAT with read-only access to all repositories, so the exporter can find new ones. It needs these permissions:

- `Metadata`: read
- `Pull requests`: read
- `Actions`: read
- `Administration`: read, for the repo settings gauges

## Blog stats

The automerge post on blog.mattjarrett.dev shows a live count of Renovate PRs merged since 2026-09-06. [scripts/blog-stats.sh](scripts/blog-stats.sh) computes it with the same merge rules as the exporter, and [the blog-stats workflow](.github/workflows/blog-stats.yml) runs it hourly on GitHub-hosted runners. It writes `stats.json` to a public gist, which the post fetches in the browser through the GitHub API. One GraphQL search returns every PR with how it merged, so nothing is cached between runs, and a run that changes nothing writes nothing. Search returns at most 1,000 results, so the script searches one 30-day window at a time.

`stats.json` also carries `days`, merges per America/Chicago day split into automatic and by hand, starting 30 days before go-live. Those earlier days include Dependabot PRs, since Dependabot opened most dependency PRs then. The homelab section on mattjarrett.dev charts them through [homelab-stats](https://github.com/cujarrett/homelab-stats).

It runs outside the cluster, so nothing in the cluster faces readers. Reading PRs uses the workflow's own token, because every repo with Renovate PRs is public. Writing the gist needs a token of its own.

### Setting it up

1. Create the gist the post reads. The ID is the last part of the URL it prints.
   ```bash
   echo '{}' > stats.json
   gh gist create --public stats.json -d "Renovate merge count for blog.mattjarrett.dev"
   ```
2. Create the token at **GitHub → Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate new token**.
   - Name: `blog-stats-gist`
   - Expiration: one year
   - Repository access: **Public repositories**
   - Account permissions: **Gists: Read and write**, nothing else
3. Store both on this repo. The secret prompts for the token so it never lands in shell history.
   ```bash
   gh secret set BLOG_STATS_GIST_TOKEN -R cujarrett/github-exporter
   gh variable set BLOG_STATS_GIST_ID -R cujarrett/github-exporter --body <gist-id>
   ```
   Both also show under **Settings → Secrets and variables → Actions**, the secret on the Secrets tab and the ID on the Variables tab.
4. Run it once instead of waiting for the schedule, then check the gist has a `stats.json` with counts.
   ```bash
   gh workflow run blog-stats -R cujarrett/github-exporter
   gh run watch -R cujarrett/github-exporter
   ```

### Rotating `BLOG_STATS_GIST_TOKEN`

When it expires the workflow fails on the gist read and the post keeps showing the last count. Generate a new token with the same settings as step 2 and run `gh secret set BLOG_STATS_GIST_TOKEN -R cujarrett/github-exporter` again. Nothing else changes.
