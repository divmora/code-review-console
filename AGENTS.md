# AGENTS.md — Divmora Code Review Console

Org-wide rules: [divmora/.github AGENTS.md](https://github.com/divmora/.github/blob/main/AGENTS.md).
License: **BSL 1.1** (Tier 2). The engine it drives, `code-review-bot`, is Apache 2.0; never copy
console code into the bot repo.

## Layout

```
cmd/console/          serve, check-config, version
internal/config/      console config: engine keys (models as plain maps, review passed through) +
                      server/db/admin/executor; per-repo Overrides (the CLI's JSON contract)
internal/executor/    runs the codereview CLI: local.go, docker.go, kubernetes.go (one contract)
internal/hosts/       code host registry (GitHub, GitLab, Bitbucket): seeded from the config file,
                      then managed in the admin panel; secrets sealed with internal/secrets
internal/secrets/     AES-256-GCM with CODEREVIEW_CONSOLE_SECRET_KEY (never stored)
internal/settings/    runtime settings: config file seeds the database, admin panel edits
internal/server/      webhooks (GitHub, GitLab, Bitbucket), command parsing, queue + history, jobs.go
                      (targets, per-job config/secrets), status dashboard
internal/admin/       OIDC sign-in (code flow + PKCE + nonce), DB sessions, CSRF, repo/audit API
internal/store/       PostgreSQL: repos, overrides, jobs + results, sessions, audit, model
                      profiles, settings; migrations under an advisory lock. storetest/ = fake
```

## The executor contract

The console never imports the review engine; it runs the CLI. The only Go import from
code-review-bot is `pkg/vcs` (webhook-side API calls). Per job:
- args from `executor.Args` (no secrets, ever);
- env from `executor.Env`: `CODEREVIEW_CONFIG_YAML` (built by `server.jobConfig`: secrets moved
  to env vars and referenced by name), `CODEREVIEW_OVERRIDES`, tokens, provider keys;
- stdout = JSON result (`executor.ParseOutput`), stderr = logs; Kubernetes uses
  `CODEREVIEW_RESULT_MARKER` because pod logs merge both streams;
- exit code 0 = success.
- `console check-config` runs kind `validate` (`codereview config validate --format json`),
  which rejects unknown keys: teach the console a new engine key only after the bot supports it.
Changing this contract means changing the bot's CLI too; keep them in step.

## Invariants

- **Job isolation**: the local executor starts from a clean env (never the console's DB URL,
  OIDC secret or GitHub App key); Docker passes secret values via the client env, not argv;
  Kubernetes Secrets are deleted after the run (owner reference as backstop).
- **One review per PR at a time**; pending jobs for a PR merge (full review wins); never cancel a
  running review mid-post.
- **Allowlist fails closed**: unknown repos are recorded as `inherit`; with no matching rule they
  are not reviewed, and store errors (repos or rules) mean "not reviewed". Precedence lives in
  `store.Resolve` only: repository mode, then the most specific rule, then the default.
- **Admin writes are audited** in the same transaction. Admin overrides win over
  `.codereview.yaml`; admin-chosen models must be configured profiles.
- **Admin auth**: verify ID-token signature, audience, nonce and allow rules; unverified emails
  never match email/domain rules.
- Model profiles stored in the DB never contain API keys (only `api_key_env`).
- Host secrets are encrypted before the store sees them; APIs and audit entries expose only
  whether a secret is set. Webhooks are verified against the matched host's secrets only.

## Tests

`make test` (no services), `make test-db` (Postgres), `make test-e2e` (real CLI, local + Docker).
Kubernetes e2e: create a kind cluster with its own kubeconfig, `kind load docker-image`, then run
`TestEndToEnd` with `CRB_E2E_KUBECONFIG`, `CRB_E2E_IMAGE` and `CRB_E2E_HOST` (the kind network
gateway). Never point tests at a real cluster.
