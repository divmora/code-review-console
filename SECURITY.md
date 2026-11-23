# Security Policy

Report vulnerabilities privately to `security@divmora.com` or via a private advisory at
[github.com/divmora/code-review-console/security/advisories/new](https://github.com/divmora/code-review-console/security/advisories/new).
We acknowledge within 48 hours and coordinate disclosure with you.

## Supported versions

Security fixes go into the latest minor release (currently **0.13.x**). Upgrade to it to get them;
older releases are not patched.

## Published advisories

| Advisory | Severity | Fixed in |
|---|---|---|
| [GHSA-6px6-j4cj-6wvh](https://github.com/divmora/code-review-console/security/advisories/GHSA-6px6-j4cj-6wvh): signed GitLab webhook replay protection was per-process memory only | Low | 0.8.0 |
| [GHSA-gjc8-7q2j-gf22](https://github.com/divmora/code-review-console/security/advisories/GHSA-gjc8-7q2j-gf22): learnings from any comment author applied immediately | High | 0.7.1 |
| [GHSA-gvq6-qjgm-vqpg](https://github.com/divmora/code-review-console/security/advisories/GHSA-gvq6-qjgm-vqpg): admin sessions were not re-authorized after sign-in rules changed | Medium | 0.7.1 |
| [GHSA-4c2p-rp59-vq7r](https://github.com/divmora/code-review-console/security/advisories/GHSA-4c2p-rp59-vq7r): GitLab webhook payload parsed and recorded before authentication | Medium | 0.7.0 |

## Operating the console safely

- **Webhooks**: every hook has its own URL and secret (Code hosts → Manage webhooks); there is no shared URL. GitHub
  and Bitbucket deliveries are verified with HMAC-SHA256, GitLab ones with a signing token (preferred, GitLab 19.0+) or
  a secret token, compared in constant time. Deliveries are authenticated before their body is parsed, and replays are
  rejected across replicas and restarts. Rotate a webhook's secret on its own if it leaks.
- **GitHub App permissions**: Pull requests read & write, Contents read, Metadata read. Installation tokens are minted per job.
- **Executors**: prefer `docker` or `kubernetes` so each review of untrusted PR content runs isolated. Mounting the Docker
  socket into the console gives it control of the host; use a dedicated host or the Kubernetes executor in shared environments.
  For Kubernetes, give the console a namespace-scoped Role limited to Jobs, Pods (get/list/logs) and Secrets.
- **Admin**: HTTPS only; restrict `allowed_groups`/`allowed_emails`; keep `session_ttl` and the inactivity sign-out
  short; review the audit log; consider exposing the admin UI only internally and the webhooks publicly. Sessions are
  rechecked against the sign-in rules on every request, and **Settings → Signed-in admins** signs sessions out. Give
  people the operator or viewer role where they don't need to change code hosts, secrets or settings.
- **Learnings**: keep "Apply new learnings at once" off unless you trust everyone who can push; learnings from people
  without write access always wait for an admin.
- **Approvals**: if the bot approves merge requests (Settings → Approve merge requests), don't let its approval alone
  satisfy required-approval rules for sensitive repositories; use rules to turn it off there.
- **Database**: TLS (`sslmode=require` or stricter), a dedicated user. Session IDs are stored as SHA-256 hashes; model
  profiles never store API keys.
- **Secrets in config**: use `${ENV_VAR}` references or `api_key_env`; keep `console.yaml` out of git.
