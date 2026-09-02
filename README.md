# github-exporter

Polls the GitHub API for merged pull requests across a fixed list of repos and exposes Prometheus counters split by repo and merge category (`human`, `dependabot-auto`, `dependabot-manual`), so Grafana can compare auto-merged Dependabot volume against everything a person still has to review.

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
| `github_exporter_pr_merged_total` | counter | `repo`, `category` | Merged PRs since the exporter started. `category` is `human`, `dependabot-auto` (grouped minor/patch, eligible for auto-merge), or `dependabot-manual` (majors, always reviewed by hand) |

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_TOKEN` | yes | | Token with read access to the tracked repos |
| `REPOS` | yes | | Comma-separated repo names to poll, e.g. `homelab,my-vinyl,launchpad` |
| `GITHUB_ORG` | no | `cujarrett` | GitHub org/user the repos belong to |
| `POLL_INTERVAL_SECONDS` | no | `300` | How often to query the GitHub search API |
| `PORT` | no | `8080` | HTTP listen port |

## Deployment

Runs on the homelab cluster via the `Api` Crossplane composition. Image: `ghcr.io/cujarrett/github-exporter`. ARM64.

Reads its env vars from a hand-created Secret named `github-exporter-config` in the `github-exporter` namespace (see the `secretsFrom` field on the XR in `homelab-workspaces/github-exporter/github-exporter.yaml`) - create it before the first sync:

```bash
kubectl create secret generic github-exporter-config -n github-exporter \
  --from-literal=GITHUB_TOKEN=<token> \
  --from-literal=REPOS=<comma-separated-repo-list>
```
