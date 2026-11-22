# Roadmap

Living backlog; remove items once implemented, tested and merged.

## In progress

- [ ] **`/api/v1` and React admin UI** (embedded in the binary): status (running/pending/recent),
  review history with findings, repositories and per-repo settings, audit log.
- [ ] **Manual actions**: review a PR URL, full review, retry a failed job, cancel a pending one.
- [x] **Model management in the UI**: profiles (no secrets, `api_key_env` only), default and
  fallback model.
- [ ] Test a model from the Models page.
- [ ] **Admin-controlled public status page**: enable/disable and hide repo names at runtime.
- [ ] Move sign-in routes to `/auth/*` and retire the embedded `/admin` page.

## Queue (design to be discussed)

- [ ] Durable queue in PostgreSQL (`SKIP LOCKED`) so pending jobs survive restarts and several
  console instances can share work.
- [ ] Global and per-repo concurrency limits, priorities, fairness across repositories.

## Platform

- [ ] More code hosts (Bitbucket, Azure DevOps) once the engine supports them.
- [ ] GitLab group-level onboarding (to be discussed).
- [ ] Replies inside review threads; resolve fixed comments.
- [ ] Prometheus metrics; cost reporting per repo and model; per-repo token budgets.
- [ ] Helm chart for the console with the Kubernetes executor (RBAC for Jobs and Secrets).
