<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo-dark.svg">
    <img src="docs/brand/logo.svg" alt="" width="88" height="88">
  </picture>
</p>

<h1 align="center">Divmora Code Review Console</h1>

<p align="center">
  <a href="https://github.com/divmora/code-review-console/releases"><img src="https://img.shields.io/github/v/release/divmora/code-review-console?logo=github" alt="Latest Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Elastic_2.0-blue.svg" alt="License: ELv2"></a>
  <a href="https://github.com/divmora/code-review-console/actions"><img src="https://github.com/divmora/code-review-console/actions/workflows/ci.yml/badge.svg" alt="CI/CD"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/divmora/code-review-console" alt="Go Version"></a>
  <a href="https://divmora.github.io/code-review-console/"><img src="https://img.shields.io/badge/website-GitHub%20Pages-blue.svg" alt="Website"></a>
  <a href="SECURITY.md"><img src="https://img.shields.io/badge/Security-Policy-green.svg" alt="Security Policy"></a>
</p>

The control plane for [code-review-bot](https://github.com/divmora/code-review-bot). It turns the open-source, one-PR-per-run CLI into an organisation-wide review service:

- **Webhooks** from GitHub (App or token, github.com and Enterprise), GitLab (gitlab.com and self-managed, several instances) and Bitbucket Cloud, each webhook with its own URL and secret.
- **A review queue** with a global worker limit. Different PRs run in parallel; the same PR never runs twice at once, and a newer push replaces an older one that is still waiting.
- **Executors**: each review runs the `codereview` CLI as a **local process**, a **Docker container** or a **Kubernetes Job**. Every job gets its own short-lived credentials and an isolated, locked-down environment.
- **Chat commands**: `@codereview review`, `full review`, `help`, or any question in a PR comment. The handle is set per code host, with a default under Settings. Commands that run the model are accepted only from people who can push to the repository (checked with the code host; Settings can open them to anyone who can comment), and each person can run a few per hour.
- **Admin** with OIDC single sign-on: a repository allowlist with group and pattern rules, review settings per group or repository, an audit log and persistent history in PostgreSQL.

### Learnings (team memory)

When someone replies to one of the bot's review comments, the console reacts 👀, runs `codereview learn` on the comment and the reply, and, when the reply states a rule for future reviews ("we compare account numbers without leading zeros on purpose"), stores it as a learning (✅ on GitLab, 🎉 on GitHub; Bitbucket has no reactions). `@bot remember <rule>` stores one directly and confirms in a comment. Learnings live in the console's database and are managed under **Learnings**: each starts scoped to its repository and can be widened to a group, a pattern or the whole host, so one standard covers every repository of a project; edit, disable or delete them there (all audited). Every review gets the active learnings that match its repository: the reviewer follows them and the verifier drops findings they rule out. New learnings wait for an admin's approval by default. **Settings** turn capture on or off and can let new learnings apply at once, but only when their author can push to the repository (GitLab Developer or higher, GitHub write or above, checked with the code host); anyone else's, and every learning from Bitbucket, still waits. The author and their role are shown on each learning.

Replies arrive as comment events: on GitLab add a group or project webhook with **Comments** (system hooks do not send comments), on GitHub subscribe to **Pull request review comments**.

### Reviews, history and metrics

The **Reviews** page lists every finished review, answer and help job from the database, with filters (host, repository, state, kind, trigger, dates). A review's details show its summary, scores, findings, what verification removed, and errors; **Re-run** reviews the same pull request again (new changes or full), and **Review now** reviews any PR/MR by URL on a configured host (allowlist, reviewer and branch-sync rules do not apply to these explicit requests).

Charts and totals read pre-aggregated rollups (hourly, daily, monthly per host and repository) that are updated as each job finishes, so they never scan the history. **Budgets** (Settings) cap tokens per UTC day for all repositories and per repository; when one is used up, automatic reviews and chat commands are skipped with a note until the next day, while "Review now" and re-runs still run. Review history is kept for 180 days by default and webhook deliveries for 14 (both in **Settings**); hourly rollups only serve the last-48-hours chart and are kept 3 days; daily and monthly totals are kept for good.

### Code hosts (GitHub, GitLab, Bitbucket)

Hosts are managed under **Code hosts** in the admin panel: github.com or GitHub Enterprise (token or GitHub App), gitlab.com or self-managed GitLab, and Bitbucket Cloud (access token, or account email plus API token). Under **Manage webhooks**, each host gets one webhook per hook you set up (a GitLab system hook, each group hook). The console generates its URL (`/webhooks/<platform>/<uuid>`) and its own secret, and lists the events to send. A **Test** button checks the token. Every delivery comes in on a webhook's own URL; the shared `/webhooks/github`, `/webhooks/gitlab` and `/webhooks/bitbucket` URLs were removed in 0.12 (see [Upgrading](docs/deploy.md#upgrading)).

Set `CODEREVIEW_CONSOLE_SECRET_KEY` (32 random bytes, `openssl rand -base64 32`, kept outside the database): tokens, App keys, webhook secrets and signing tokens are encrypted with it (AES-256-GCM) before they are stored, and the admin panel never shows them again. Losing the key means entering the secrets again. The key and `DATABASE_URL` are required. `console hosts export` and `console hosts import` move hosts and webhooks as YAML ([docs/deploy.md](docs/deploy.md#moving-code-hosts)).

### Several GitLab instances and hooks

One console serves any number of GitLab instances. Repositories, rules and webhook history are keyed by host, so `acme/app` on `gitlab.x.example` and on `gitlab.y.example` are separate, and so are their rules. Each instance has its own token, and so its own bot user; set the bot name (mention handle) and reviewer (the username that counts for the reviewer rule) per host when they differ from the defaults.

Webhooks can come from a system hook (self-managed GitLab, every project), group hooks and project hooks, alone or mixed. Add one webhook in the console for each, with its own URL and secret or signing token. When one event arrives through several hooks, it is processed once and the extra deliveries are recorded as duplicates. Note that system hooks do not send comment events: add a group or project hook with **Comments** for `@bot` commands.

### Review only when the bot is a reviewer

With **Who asks for a review: only when the bot is a reviewer** (Settings), a merge or pull request is reviewed only when the bot user (the user of the GitLab token or GitHub token) is one of its reviewers, so webhooks from many repositories cost nothing until someone asks. Adding the bot as a reviewer starts a review; new commits are reviewed while it stays a reviewer; removing it stops automatic reviews. `@codereview review` comments still work. The setting applies on top of the allowlist and rules, so you can turn the allowlist off and let reviewer requests decide.

On GitHub, a submitted review clears the request, so request the bot again to review new commits. GitHub Apps cannot be requested as reviewers; use a token user for this mode.

### GitLab webhook authentication

Give each GitLab webhook a **signing token** (GitLab 19.0+) and paste it (`whsec_...`) into the webhook in the console. GitLab then signs every request (`webhook-id`, `webhook-timestamp`, `webhook-signature`, HMAC-SHA256 over `id.timestamp.body`). The console checks the signature, rejects requests more than 5 minutes old, and ignores a repeated `webhook-id`, so a captured request cannot be replayed.

Older webhooks with only a **secret token** (`X-Gitlab-Token`, sent in plain text) keep working. Once a webhook has a signing token, it accepts signed deliveries only, so the old secret token can't be used to skip the signature; turn on **Accept too** for that webhook while hooks migrate (those deliveries are marked on the Webhooks page). A signed request is judged by its signature alone: a valid secret token does not rescue a bad signature. The host's **Unsigned deliveries: Reject everywhere** makes every URL of the host signature-only.

### Which repositories are reviewed

Each repository is **Inherit** (the default for repositories that send a webhook), **Enabled** or **Disabled**. Inherit follows the most specific matching rule, and without one the default: not reviewed with the allowlist on (Settings), reviewed with it off.

Rules are managed under *Groups & patterns* in the admin UI:

| Rule | Covers |
|---|---|
| `acme` | every repository of the GitHub owner or GitLab group `acme`, GitLab subgroups included |
| `acme/platform` | the GitLab subgroup and everything under it |
| `acme/svc-*` | matching names in one level (`*` never crosses `/`) |

Deeper rules beat shallower ones and exact groups beat patterns, so `acme` enabled with `acme/legacy-*` disabled reviews everything except the legacy repositories. A repository set to Enabled or Disabled overrides every rule. Settings add up from the broadest matching rule to the most specific, then the repository's own settings; extra instructions are joined.
- **Approvals**: the bot can approve merge requests whose review passes a rule you set (which findings block it, an optional lowest score) and withdraw its approval when a later push doesn't pass; rules turn it on or off per group or repository.
- **Impact**: what the console has done for the installation (merge requests reviewed, findings by severity and category, security findings, time to first review, active repositories, scores), with trends, top repositories and a CSV/JSON export, from its own database.
- **Scores**: each review's quality, security and maintainability scores on Reviews, per repository with trends, on the status dashboard and in `/metrics`.
- **Status**: running, pending and recent reviews on a status dashboard, for signed-in admins by default; it can also be opened with a token (for a team screen) or made public (Settings → Status dashboard access).
- **Metrics**: `/metrics` in the Prometheus format (queue, reviews, durations, tokens, webhooks, license and token expiry), off until chosen under Settings.

> The `/api/v1` API, testing a model from the panel, manual actions and the admin-controlled public status page are in progress. See [ROADMAP.md](ROADMAP.md).

## How it fits together

```
GitHub / GitLab ──webhook──▶ console ──queue──▶ executor ──runs──▶ codereview CLI ──▶ comments on the PR
                               │                (local | docker | kubernetes)
                               └── PostgreSQL: allowlist, settings, history, sessions, audit log
```

For each job the console builds:
- **arguments**: the PR URL and flags;
- **config**: `CODEREVIEW_CONFIG_YAML`, the engine settings with secrets referenced as `${VAR}`, never inlined;
- **settings**: `CODEREVIEW_OVERRIDES`, the admin's per-repo settings;
- **secrets**: a GitHub App installation token or GitLab token, plus the provider keys, all as environment variables.

The CLI prints its result as JSON, which the console stores.

## Quick start

```bash
cp examples/console.env .env              # secret key and admin sign-in
docker compose up --build                 # console + PostgreSQL
```

Then sign in and follow **Setup**: code hosts, model profiles and the license are added in the admin panel. No config file is needed: the few startup settings are environment variables (`code-review-console env` lists them), and a `console.yaml` can hold them instead if you prefer.

- GitHub App setup: [docs/github-app.md](docs/github-app.md)
- GitLab webhooks: [docs/gitlab.md](docs/gitlab.md)
- Admin sign-in (OIDC): [docs/admin.md](docs/admin.md)
- Deploying, upgrading and CLI compatibility: [docs/deploy.md](docs/deploy.md)
- Check what reviews will run with: `code-review-console check-config`

## Executors

| Type | Runs each review as | Isolation | Good for |
|---|---|---|---|
| `local` | a child process with a clean environment (the console's own secrets are never passed) | process group, timeout, kill on cancel | one small server |
| `docker` | `docker run --rm` from the CLI image: read-only root, no capabilities, `no-new-privileges`, pid/CPU/memory limits | container | a single host |
| `kubernetes` | a Job with backoff 0, a deadline and a TTL; non-root, read-only root, all capabilities dropped, seccomp, resource limits; job env in a Secret deleted right after the run | pod | clusters, autoscaling, IRSA/workload identity for Bedrock |

Provider keys and cloud credentials (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `AWS_*`, ...) are forwarded from the console's environment when set; add more names under Settings → Bot and review jobs.

## Development

```bash
make build      # bin/code-review-console (builds the admin UI first if it's missing; needs Node.js)
make web        # rebuild the admin UI (web/ → internal/admin/ui, embedded; not committed)
make web-dev    # admin UI dev server on :5173, proxying to a console on :8080
make test       # unit tests (the admin tests need the built UI; make builds it)
make test-db    # + PostgreSQL integration tests (Docker)
make test-e2e   # + end-to-end with the real codereview CLI (local + docker executors)
```

The console imports only `pkg/vcs` from code-review-bot (code host API clients). Everything else about the engine goes through the CLI: the console writes `CODEREVIEW_CONFIG_YAML` itself, and `console check-config` runs `codereview config validate` through the configured executor, so the binary or image that runs reviews is the one that judges the config. This console needs code-review-bot v0.4.0 or later; see [docs/deploy.md](docs/deploy.md#cli-compatibility).

## License

**[Elastic License 2.0](LICENSE)** (ELv2). Copyright 2026 DIVMORA Technologies.

You may use, run, modify and distribute the console, including in production, free of charge, except that you may not provide it to others as a hosted or managed service, and you may not move, change, disable or circumvent the license key functionality or remove anything it protects.

| | Free core | Paid features (license key) |
|---|---|---|
| What | Reviews for one GitHub, GitLab and Bitbucket host each; local executor; repository on/off; `@bot` commands; review history for 30 days; Impact headline numbers for 30 days; status dashboard; admin sign-in by email | More hosts per platform and per-host bot identities; host, group and pattern rules; team learnings; Docker and Kubernetes executors; token budgets; metrics and long history (including the Impact page's trends, breakdowns and export); audit log viewer; branch-sync skip; SSO domain and group rules |
| Cost | Free in every environment, production included | A license key from Divmora, in every environment (development, CI, staging and production) |

Install, replace or remove the key under **License** in the admin panel; it applies at once ([docs/deploy.md](docs/deploy.md#license)). Keys are verified offline. Licenses and trials: licensing@divmora.com.

The review engine, [code-review-bot](https://github.com/divmora/code-review-bot), is Apache 2.0 and free in any environment.
