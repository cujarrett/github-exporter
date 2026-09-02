## Rules

- **Never run `git commit`, `git push`, or any git command that writes to or modifies repository history or remotes.** If a task requires committing or pushing, stop and tell the user to run the git command manually.
- **Whenever a task requires a commit, always give a suggested commit message** - never leave the user to write it themselves.

### Pre-commit safety check

Before telling the user to commit, always run `/security-review`. It reviews the pending changes on the current branch for security issues. Once it confirms the changes are safe, offer the user a suggested commit message - do not run `git commit` yourself.

## Philosophy: Grug-Brained Development

> "Complexity very, very bad." - [grugbrain.dev](https://grugbrain.dev/)

- **Say no.** The best weapon against complexity is the word "no". No new feature, no new abstraction, until it earns its place.
- **No abstraction until a pattern repeats three times.** Let cut points emerge naturally from the code; don't invent them up front.
- **80/20 solutions.** Ship 80% of the value with 20% of the code. Ugly but working beats elegant but over-engineered.
- **Chesterton's Fence.** Understand why code exists before removing it. If you don't see the use, go away and think.
- **Boring, obvious code wins.** Intermediate variables with good names beat clever one-liners. Easier to debug.
- **DRY is not a law.** A little copy-paste beats a complex abstraction built for two cases.
- **No FOLD** (Fear Of Looking Dumb). If something is too complex, say so. That's a signal to simplify, not a personal failing.

# github-exporter

Go Prometheus exporter, single binary, no frameworks. Polls the GitHub search API for merged PRs and counts them by repo and merge category.

## Commands
| Command | What it does |
|---|---|
| `just ci` | Lint + test + build (run before pushing) |
| `just run` | Start the server locally on port 8080 |
| `just test` | Run tests with race detector |
| `just lint` | go mod tidy -diff + golangci-lint |

## Routes
| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Liveness probe |
| GET | `/metrics` | Prometheus metrics |

## Conventions
- No frameworks - stdlib `net/http` only, including for the outbound GitHub call. See [README.md](README.md) for why `go-github` was rejected: this repo's own Dependabot would then have to track a dependency that exists only to save ~15 lines of hand-rolled JSON decoding.
- `slog` for structured logging
- Graceful shutdown via `signal.NotifyContext`
- Errors returned as `{"error":"..."}` JSON where the API surfaces errors (only `/healthz` does today)
