# Security Policy

Report vulnerabilities privately to `security@divmora.com` or via a private advisory at
[github.com/divmora/code-review-console/security/advisories/new](https://github.com/divmora/code-review-console/security/advisories/new).
We acknowledge within 48 hours and coordinate disclosure with you.

## Operating the console safely

- **Webhook secrets are mandatory**; GitHub payloads are verified with HMAC-SHA256, GitLab tokens with a constant-time compare.
- **GitHub App permissions**: Pull requests read & write, Contents read, Metadata read. Installation tokens are minted per job.
- **Executors**: prefer `docker` or `kubernetes` so each review of untrusted PR content runs isolated. Mounting the Docker
  socket into the console gives it control of the host; use a dedicated host or the Kubernetes executor in shared environments.
  For Kubernetes, give the console a namespace-scoped Role limited to Jobs, Pods (get/list/logs) and Secrets.
- **Admin**: HTTPS only; restrict `allowed_groups`/`allowed_emails`; keep `session_ttl` short; review the audit log;
  consider exposing the admin UI only internally and the webhooks publicly.
- **Database**: TLS (`sslmode=require` or stricter), a dedicated user. Session IDs are stored as SHA-256 hashes; model
  profiles never store API keys.
- **Secrets in config**: use `${ENV_VAR}` references or `api_key_env`; keep `console.yaml` out of git.
