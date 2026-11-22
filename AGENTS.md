# AGENTS.md — Divmora Code Review Console

Org-wide rules: [divmora/.github AGENTS.md](https://github.com/divmora/.github/blob/main/AGENTS.md).
License: **Elastic License 2.0** (one license for the whole repo). Paid features are protected by
license keys, and ELv2 forbids circumventing them. The engine it drives, `code-review-bot`, is
Apache 2.0; never copy console code into the bot repo.

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
internal/admin/       OIDC sign-in (code flow + PKCE + nonce), DB sessions, CSRF, admin JSON API;
                      embeds ui/ (the built admin app, committed so `go build` needs no Node)
web/                  admin app source: React + TypeScript + Vite. `make web` builds it into
                      internal/admin/ui; CI fails if the committed build is stale
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
- Model profiles stored in the DB never contain API keys (only `api_key_env`). internal/models
  merges them with the config file's (DB wins) and passes each DB profile's api_key_env value
  to jobs; names holding the console's own secrets (CODEREVIEW_CONSOLE_*, host tokens) are refused.
- Learnings are stored and scoped by the console; the CLI only uses them (CODEREVIEW_OVERRIDES)
  and extracts them (`codereview learn`). The console never calls a model itself.
- Host secrets are encrypted before the store sees them; APIs and audit entries expose only
  whether a secret is set. A delivery on `/webhooks/<platform>/<uuid>` (a webhook endpoint added in
  the admin panel) is routed by the URL and verified against that endpoint's secrets only; the shared
  `/webhooks/<platform>` URL matches the host from the delivery and uses the host's secrets only.
  A generated endpoint secret is returned once (create or regenerate) and never again.

## Licensing (free core + license keys)

- Paid features are listed in `internal/ee/license` (`license.All`). The license is a Divmora DIV1
  token (github.com/divmora/license-go) for product `code-review-console`, issued by the license
  portal and verified offline against the keys in `internal/ee/license/keys.go`.
- A nil `*license.Manager` is the free core. Gate at one place per feature:
  `settings.Runtime` (learnings, budgets, branch-sync skip, retention), rules dropped before
  `store.Resolve`, `hosts.Registry.Enabled`/`CheckLicense`, admin APIs (`a.require` → 402), the
  executor at startup, SSO rules in `authorize`. The admin panel shows and saves stored values;
  runtime ignores the unlicensed ones, so a renewed license restores them.
- New paid features: add to `license.All`/`Titles`, the README table, the UI (`featureTitles`,
  `useFeature`, `<Locked>`) and the portal's product entry (license-portal `products.json`).
- Tests: `licensetest.Full(t)` for licensed setups; keep a free-core test for every gate.

## Admin UI

React + TypeScript + Vite, styled with **Tailwind CSS v4 and shadcn/ui** (Radix primitives, Lucide
icons). Change `web/src`, run `make web`, commit both `web/` and `internal/admin/ui`.

- `web/src/components/ui/` holds shadcn components (`npx shadcn@<pinned> add <name>`). After adding
  one, check its imports: utilities come from `@/lib/utils`. The CLI has written `from "cn"` and
  installed an unrelated npm package by that name.
- Shared pieces live in `web/src/ui.tsx`: `Panel` (table card), `SidePanel` (editors and details),
  `Field`/`FormGrid`, `NativeSelect` and `Tri` (native selects for Inherit/On/Off), `StateBadge`,
  `Locked`/`LockNote` (license), `useConfirm` (confirm dialogs), toasts. Reuse them, not new CSS.
- Theme colours are CSS variables in `web/src/index.css` (`.dark` class, set by
  `public/assets/theme.js`). Use tokens (`text-muted-foreground`, `bg-success-soft`), not hex colours.
- Strict CSP: `script-src 'self'`; `style-src 'self'` plus a per-request nonce. The server
  replaces `__CSP_NONCE__` in `index.html` (`handlePage`), and `main.tsx` passes it to `get-nonce`
  for the `<style>` tag Radix adds to lock scroll under dialogs and sheets. No inline scripts and no
  `style=` attributes in HTML; React's `style` prop and `el.style` are fine. Don't add libraries that
  inject `<style>` tags unless they read the nonce.
- The CSRF token comes from `GET /admin/api/me`. New sections need a route in `web/src/App.tsx` and
  an entry in `adminSections` (handlers.go) so reloads land on them.

## Tests

`make test` (no services), `make test-db` (Postgres), `make test-e2e` (real CLI, local + Docker).
Kubernetes e2e: create a kind cluster with its own kubeconfig, `kind load docker-image`, then run
`TestEndToEnd` with `CRB_E2E_KUBECONFIG`, `CRB_E2E_IMAGE` and `CRB_E2E_HOST` (the kind network
gateway). Never point tests at a real cluster.
