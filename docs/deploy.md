# Deploying the console

Each release publishes:

| Artifact | Where | Contains |
|---|---|---|
| Container image | `ghcr.io/divmora/code-review-console:X.Y.Z` (linux/amd64, linux/arm64) | the console, the admin UI, and the pinned `codereview` CLI for the local executor |
| Binaries | the GitHub release (Linux and macOS; amd64, arm64). There is no Windows build: use the container image there | the console only; install the CLI from [code-review-bot](https://github.com/divmora/code-review-bot/releases) |

Both include every feature; a license key turns the paid ones on (see [License](#license)).

The admin UI is built into the binary: there is nothing else to serve.

## CLI compatibility

The console runs the `codereview` CLI for every review, so the two must agree on the
`CODEREVIEW_OVERRIDES` keys and commands. Each console release states the oldest CLI it supports
(`MinCLIVersion` in `internal/config/cli.go`):

| Console | code-review-bot CLI |
|---|---|
| 0.2.0 and later | v0.4.0 or later |

- The container image already contains a supported CLI.
- With the `kubernetes` executor, use v0.4.1 or later: from then on the CLI writes its logs to the
  same stream as the result, so they can't end up inside it (the console also repairs results
  from older CLIs).
- With the `docker` or `kubernetes` executor, `executor.*.image` is the CLI image. A release tag
  older than the minimum is rejected when the config loads.
- Always run `code-review-console check-config --config console.yaml` after an upgrade. It asks
  the CLI the executor actually runs to validate the config, and fails if that CLI is too old.
  With `database.url` it uses what reviews use: the executor settings (version, resources,
  scheduling) and model profiles saved in the admin panel. It only reads the database: no
  migrations, nothing written.

## License

Without a key the console runs the **free core**: reviews for one host per platform, the local
executor, repository on/off, `@bot` commands, 30 days of history, the dashboard and admin sign-in
by email. Paid features need a Divmora license key (a `DIV1.…` token, or the armored
`-----BEGIN DIVMORA LICENSE KEY-----` block) for product `code-review-console`. The README lists
the features.

**Manage the key under License in the admin panel**: paste it to install or replace it, or remove it.
It is stored in the database and audited without the key itself, and applies at once, with no
restart. That includes the docker and kubernetes executors: without the `executors` feature the
console still starts, shows "reviews are paused" in the panel and on **Setup**, and refuses review
jobs with a clear error until the key is added.

Two other sources exist:

- `CODEREVIEW_CONSOLE_LICENSE`: an **override**, for example when nobody can sign in. While it is
  set, it wins over the panel, and the panel shows the key as managed by the variable.
- `license.file` (**deprecated**): when the database has no key yet, a valid key from the file is
  copied into it on start, and from then on the panel manages it. Afterwards the file is ignored,
  and the console logs a warning until you remove `license.file`. Without a database, the file is
  read as before.

Keys are verified offline against Divmora's public key; nothing is sent anywhere.

When a license expires, paid features keep working for its grace period (14 days unless
the license says otherwise), with a banner in the admin panel. After that the console goes back
to the free core. It never stops reviewing, and settings for paid features are kept for when the
license is renewed.

## Environment

| Variable | Needed for |
|---|---|
| `DATABASE_URL` | PostgreSQL 13+ (`database.url: ${DATABASE_URL}`). Required for the admin panel, the allowlist, learnings and history. Migrations run at startup under an advisory lock, so several replicas can start together. Replicas share webhook replay protection through it: a delivery is processed once, whichever replica receives it. |
| `CODEREVIEW_CONSOLE_LICENSE` | The Divmora license key (see [License](#license)) |
| `CODEREVIEW_CONSOLE_SECRET_KEY` | Managing code hosts in the admin panel. 32 bytes as base64 or hex (`openssl rand -base64 32`), or a passphrase of 32+ characters. Host tokens and webhook secrets are encrypted with it. **Keep it safe:** if it is lost, the stored tokens cannot be read and must be entered again. Without it, hosts come from the config file and are read-only. |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | Admin sign-in ([admin.md](admin.md)) |
| `GITHUB_TOKEN`, `GITLAB_TOKEN`, ... and webhook secrets | Code hosts from the config file (seeded into the database on first start when the secret key is set) |
| `CODEREVIEW_CONSOLE_REGISTRY_USERNAME`, `CODEREVIEW_CONSOLE_REGISTRY_PASSWORD` | Optional: read-only credentials for a private image registry, so the **Executor** page can list the versions in your repository (public ones such as ghcr.io need none). Without them, or offline, you type the version. Amazon ECR's 12-hour passwords aren't refreshed, so type the version there. |
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
    verbs: ["create", "patch", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: [""]               # optional, see below
    resources: ["events"]
    verbs: ["list"]
```

Bind it to the console's service account with a RoleBinding. `patch` on secrets lets the console
make each job's Secret owned by its Job, so Kubernetes deletes the Secret with the Job even if the
console stops mid-review. Without it the console logs a warning and the Secret, which holds the job's
tokens and provider keys, can be left behind. `list` on events is optional: with it, a review pod that
Kubernetes refuses to create (a ResourceQuota, a LimitRange, an admission webhook) fails the review
at once with the reason; without it, the review waits for its deadline.

Leave `executor.kubernetes.kubeconfig` empty to use the in-cluster config. Set
`executor.kubernetes.service_account` only when review jobs need a cloud identity (for example IRSA,
EKS Pod Identity or GKE Workload Identity for Bedrock or Vertex). These don't need the Kubernetes API
token, so review pods never get one by default, with or without a service account. Set
`executor.kubernetes.automount_service_account_token: true` only if your setup really needs the API
token in review pods. Review pods run untrusted PR content, so keep RBAC bound to that service account
to a minimum.

### Settings in the admin panel

With a database, the **Executor** page manages the image version (a tag, a digest, or both as
`0.5.0@sha256:…`; the repository stays in the config file), CPU and memory, and for Kubernetes the disk (ephemeral storage, 2Gi by default) and /tmp size (1Gi by default), the job TTL, node selector, tolerations,
affinity, priority class, pod labels and annotations, and the CA bundle below. On the first start
they are copied from the config file; from then on the panel's values win. The version is picked
from the release tags found in the image repository (your own registry or mirror works: the console
lists its tags with the registry API), or typed. The log names any config file value that is
ignored, and a change applies to the next review without a restart.

The executor type, binaries, the Docker network, `kubeconfig`, `namespace`, `service_account`,
`automount_service_account_token` and `env` stay in the config file only: they decide what review
jobs, which carry the code host tokens and model keys, can reach.

### Where review pods run

Optional fields under `executor.kubernetes` are copied into each review Job's pod template:

```yaml
executor:
  kubernetes:
    node_selector: { workload: batch }
    tolerations:
      - { key: dedicated, operator: Equal, value: batch, effect: NoSchedule }
    affinity:                          # a Kubernetes Affinity, same field names as in a pod spec
      nodeAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          nodeSelectorTerms:
            - matchExpressions: [{ key: kubernetes.io/arch, operator: In, values: [amd64] }]
    priority_class_name: batch-low     # the PriorityClass must exist
    pod_labels: { team: platform }
    pod_annotations: { karpenter.sh/do-not-disrupt: "true" }
```

`tolerations` and `affinity` use the Kubernetes field names (`tolerationSeconds`, `nodeAffinity`,
...). An unknown field stops startup with an error, so a typo doesn't silently drop a rule.
`pod_labels` are merged with the console's own labels, which win on conflict because the console
finds review pods by them. With Karpenter, `karpenter.sh/do-not-disrupt: "true"` keeps
consolidation from evicting a review mid-run.

### Private CA certificates

When a self-managed GitLab or GitHub Enterprise, an LLM gateway or a TLS-inspecting proxy uses a
certificate from a private CA, put the CA certificates (PEM) in a ConfigMap or Secret in the jobs'
namespace and point `ca_bundle` at it:

```bash
kubectl -n code-review create configmap corporate-ca --from-file=ca.crt=corporate-ca.pem
```

```yaml
executor:
  kubernetes:
    ca_bundle:
      config_map: corporate-ca         # or secret: corporate-ca
      key: ca.crt                      # default ca.crt
```

The key is mounted read-only at `/etc/codereview/ca/ca.crt`, and `SSL_CERT_DIR` points at that
directory. These certificates are trusted **in addition to** the image's public CAs, so the bundle
only needs your own CAs. No extra RBAC is needed: the kubelet mounts the volume, not the console.

Point the Deployment's liveness and readiness probes at `GET /healthz` on the server port.

## Moving code hosts

Code hosts and their webhooks are managed in the admin panel. The `github:`, `gitlab:` and
`bitbucket:` sections of the config file are **deprecated**: they only seed the database on first
start, and will be removed in a future release. The console logs a warning while they are present.

To keep hosts in Git, move them to another install, or change them when nobody can sign in, use the
`hosts` commands. They need `database.url` and `CODEREVIEW_CONSOLE_SECRET_KEY`, like `serve`:

```bash
console hosts export > hosts.yaml                  # secrets left out
console hosts export --with-secrets -o hosts.yaml  # full copy, clear text: keep it out of Git
console hosts import --dry-run -f hosts.yaml       # show what would change
console hosts import -f hosts.yaml
```

```yaml
hosts:
  - platform: gitlab
    base_url: https://gitlab.example.com
    name: Our GitLab
    token: ${GITLAB_TOKEN}            # ${VAR} is read from the environment
    settings: { bot_name: codereview }
    webhooks:
      - id: 0b6c2f9e-5b1d-4c2a-9f0e-1d2c3b4a5f60   # keep the id to keep the URL
        name: platform group
        secret: ${GITLAB_HOOK_PLATFORM}
      - name: web group                # no id or secret: both are generated, the secret printed once
```

Import creates or updates hosts (matched by platform and URL) and webhooks (matched by id), and
never deletes. An absent secret keeps the stored value. The whole file is checked before anything is
written, and unknown fields are rejected. Imports are audited as `cli: hosts import`.

## Upgrading

1. Read the release notes for a new minimum CLI version.
2. Update the image or binary. For `docker` or `kubernetes` executors, change the CLI version on the
   **Executor** page (or the image tag in the config file when there is no database).
3. Run `check-config`.
4. Restart. Database migrations apply automatically. On SIGTERM the console stops taking work and
   gives running reviews up to 2 minutes to finish; give the container at least that long
   (Kubernetes `terminationGracePeriodSeconds: 150`). The queue is in memory: reviews still
   waiting are dropped, and the next push, `@bot review` or **Review now** queues them again.
