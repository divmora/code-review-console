# Deploying the console

Each release publishes:

| Artifact | Where | Contains |
|---|---|---|
| Container image | `ghcr.io/divmora/code-review-console:X.Y.Z` (linux/amd64, linux/arm64) | the console, the admin UI, and the pinned `codereview` CLI for the local executor |
| Binaries | the GitHub release (linux, macOS, Windows; amd64, arm64) | the console only; install the CLI from [code-review-bot](https://github.com/divmora/code-review-bot/releases) |

Both include the free core and the enterprise features; a license key turns the latter on.

The admin UI is built into the binary: there is nothing else to serve.

## CLI compatibility

The console runs the `codereview` CLI for every review, so the two must agree on the
`CODEREVIEW_OVERRIDES` keys and commands. Each console release states the oldest CLI it supports
(`MinCLIVersion` in `internal/config/cli.go`):

| Console | code-review-bot CLI |
|---|---|
| 0.2.0 and later | v0.4.0 or later |

- The container image already contains a supported CLI.
- With the `docker` or `kubernetes` executor, `executor.*.image` is the CLI image. A release tag
  older than the minimum is rejected when the config loads.
- Always run `code-review-console check-config --config console.yaml` after an upgrade. It asks
  the CLI the executor actually runs to validate the config, and fails if that CLI is too old.

## License

Without a key the console runs the **free core**: reviews for one host per platform, the local
executor, repository on/off, `@bot` commands, 30 days of history, the dashboard and admin sign-in
by email. Paid features need a Divmora license key (a `DIV1.…` token, or the armored
`-----BEGIN DIVMORA LICENSE KEY-----` block) for product `code-review-console`. The README lists
the features.

The key is read from the first of these that is set:

1. `CODEREVIEW_CONSOLE_LICENSE`: the key itself
2. `license.file` in the config: a file holding the key (for example a mounted Secret)
3. the key pasted under **License** in the admin panel (stored in the database, audited without
   the key itself)

Keys are verified offline against Divmora's public key; nothing is sent anywhere. A new key
applies at once, except for the executor type, which is checked when the console starts:
`executor.type: docker` or `kubernetes` without the `executors` feature stops startup with a clear
error. When a license expires, paid features keep working for its grace period (14 days unless
the license says otherwise), with a banner in the admin panel. After that the console goes back
to the free core. It never stops reviewing, and settings for paid features are kept for when the
license is renewed.

## Environment

| Variable | Needed for |
|---|---|
| `DATABASE_URL` | PostgreSQL 13+ (`database.url: ${DATABASE_URL}`). Required for the admin panel, the allowlist, learnings and history. Migrations run at startup under an advisory lock, so several replicas can start together. |
| `CODEREVIEW_CONSOLE_LICENSE` | The Divmora license key (see [License](#license)) |
| `CODEREVIEW_CONSOLE_SECRET_KEY` | Managing code hosts in the admin panel. 32 bytes as base64 or hex (`openssl rand -base64 32`), or a passphrase of 32+ characters. Host tokens and webhook secrets are encrypted with it. **Keep it safe:** if it is lost, the stored tokens cannot be read and must be entered again. Without it, hosts come from the config file and are read-only. |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | Admin sign-in ([admin.md](admin.md)) |
| `GITHUB_TOKEN`, `GITLAB_TOKEN`, ... and webhook secrets | Code hosts from the config file (seeded into the database on first start when the secret key is set) |
| Provider keys (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `AWS_*`, ...) | Forwarded to each review job; add other names with `executor.env` |

`GET /healthz` returns `{"status":"ok","version":...,"pending":N,"running":N}` for probes.

## Docker Compose

```bash
cp examples/console.yaml console.yaml     # models, hosts, admin
cat > .env <<'EOF'
POSTGRES_PASSWORD=change-me
CODEREVIEW_CONSOLE_SECRET_KEY=<openssl rand -base64 32>
OIDC_CLIENT_SECRET=...
EOF
docker compose up -d
docker compose exec console code-review-console check-config --config /etc/console/console.yaml
```

To run a published image instead of building, replace `build: .` with
`image: ghcr.io/divmora/code-review-console:X.Y.Z` in `docker-compose.yml`.

## Kubernetes

Run the console as a Deployment with the `kubernetes` executor, so each review is its own Job.
The console's service account needs only these permissions, in the namespace where the jobs run:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: code-review-console, namespace: code-review }
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "delete"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["create", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

Bind it to the console's service account with a RoleBinding. Leave `executor.kubernetes.kubeconfig`
empty to use the in-cluster config. Set `executor.kubernetes.service_account` only when review jobs
need a cloud identity (for example IRSA or workload identity for Bedrock). Without it, job pods get
no service account token.

Point the Deployment's liveness and readiness probes at `GET /healthz` on the server port.

## Upgrading

1. Read the release notes for a new minimum CLI version.
2. Update the image or binary, and for `docker` or `kubernetes` executors, the CLI image tag.
3. Run `check-config`.
4. Restart. Database migrations apply automatically. On SIGTERM the console stops taking work and
   gives running reviews up to 2 minutes to finish; give the container at least that long
   (Kubernetes `terminationGracePeriodSeconds: 150`). The queue is in memory: reviews still
   waiting are dropped, and the next push, `@bot review` or **Review now** queues them again.
