# github-exporter

Polls the GitHub API for merged pull requests across a fixed list of repos and exposes Prometheus gauges split by repo and merge category (`human`, `dependabot-auto`, `dependabot-manual`), so Grafana can compare auto-merged Dependabot volume against everything a person still has to review.

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
| `github_exporter_pr_merged` | gauge | `repo`, `author`, `merge`, `window` | Merged PRs inside a window, recounted every poll. `author` is `human` or `dependabot`. `merge` is how it reached main, read from GitHub's own record rather than the title: `auto` (GitHub merged it once checks passed), `clicked` (a person pressed the button), `bot` (a workflow merged with its own token). `window` is `1d` for a trend line and the configured lookback (`30d` by default) for the headline count |
| `github_exporter_workflow_runs` | gauge | `repo`, `conclusion` | Workflow runs on the default branch inside the lookback window. `conclusion` is `success`, `failure`, `cancelled` or `skipped`. Runs still in flight carry no conclusion and are not counted |
| `github_exporter_poll_errors_total` | counter | `repo` | Failed GitHub polls. Alert on this - a dead token otherwise looks like a quiet week |

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_TOKEN` | in local dev | | Token with read access to the tracked repos. In the cluster it arrives as a mounted file instead |
| `GITHUB_TOKEN_FILE` | no | `/secrets/github-exporter-token/GITHUB_TOKEN` | Path to the mounted token |
| `REPOS` | no | | Comma-separated repo names. Omit to poll every active repo the account owns, rediscovered on each poll |
| `GITHUB_ORG` | no | `cujarrett` | GitHub org/user the repos belong to |
| `POLL_INTERVAL_SECONDS` | no | `300` | How often to query the GitHub search API |
| `LOOKBACK_DAYS` | no | `30` | How far back each poll counts merged PRs |
| `PORT` | no | `8080` | HTTP listen port |

## Deployment

Runs on the homelab cluster via the `Api` Crossplane composition. Image: `ghcr.io/cujarrett/github-exporter`. ARM64.

Config comes from the `github-exporter-config` ConfigMap in `homelab-workspaces`, applied by ArgoCD. The token is a hand-created Secret, since no secret value reaches git:

```bash
kubectl create secret generic github-exporter-token -n github-exporter \
  --from-literal=GITHUB_TOKEN=<token>
```

### Rotating `github-exporter-read`

A fine-grained PAT on `cujarrett`, read-only across all repositories, with `Metadata: read`, `Pull requests: read` and `Actions: read`. All repositories rather than a list, because the exporter discovers them.

```bash
print -n "Paste new token: "
read -rs NEW_TOKEN
echo
kubectl patch secret github-exporter-token -n github-exporter \
  --type='json' \
  -p='[{"op":"replace","path":"/data/GITHUB_TOKEN","value":"'"$(echo -n "$NEW_TOKEN" | base64)"'"}]'
unset NEW_TOKEN
```

No restart. The binary reads the file on each GitHub call and kubelet refreshes the mount within about a minute.

### Rotating `HOMELAB_PAT`

Separate from the token above, and easy to confuse. `github-exporter-read` is a Kubernetes Secret the running binary reads to query GitHub. `HOMELAB_PAT` is a GitHub Actions secret only CI uses to bump this image's tag in `homelab-workspaces`, shared across every repo that deploys there and rotated centrally - see [GitHub Tokens](https://github.com/cujarrett/homelab/blob/main/docs/github-tokens.md) in the homelab repo. Rotating one leaves the other alone.
