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

- **Approving merge requests** (Settings → Approve merge requests) needs v0.5.0 or later: older CLIs
  don't know `--approve`, and reviews fail while it is on. v0.5.0 also anchors scores to the
  previous review, so they don't swing between runs.
- **v0.6.1 is recommended**, and the console image bundles it. 0.6.0 brought security fixes (see the
  code-review-bot advisories):
  - no GitLab quick actions or mentions in what the bot posts;
  - only the bot's own, or signed, comments are read back as its state, using a key the console passes each job;
  - tokens go only to their configured host;
  - an approval is withdrawn when a new commit can't be judged, and files the review skipped hold approval back.

  It also changes scoring: only fixed findings raise scores, and summaries say what was fixed and dismissed.
  0.6.1 fixes 0.6.0 refusing jobs when a host's API URL and PR links use different names, such as an internal URL
  or GitHub Enterprise Cloud's `api.<name>.ghe.com`. The console tells it the PR host; avoid 0.6.0 in those setups.

- The container image already contains a supported CLI.
- With the `kubernetes` executor, use v0.4.1 or later: from then on the CLI writes its logs to the
  same stream as the result, so they can't end up inside it (the console also repairs results
  from older CLIs).
- With the `docker` or `kubernetes` executor, `executor.*.image` is the CLI image. A release tag
  older than the minimum is rejected when the config loads.
- Always run `code-review-console check-config` after an upgrade (add `--config console.yaml` if you use a file). It asks
  the CLI the executor actually runs to validate the config, and fails if that CLI is too old.
  It uses what reviews use: the executor settings (version, resources, scheduling) and model
  profiles saved in the admin panel, so it needs `database.url`. It only reads the database: no
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
  and the console logs a warning until you remove `license.file`.

Keys are verified offline against Divmora's public key; nothing is sent anywhere.

**Expiry warnings.** From 14 days before the license or a code host token expires, every admin page
shows a banner with the date and what stops, the matching Setup step turns amber, and the server
logs a warning once a day. A host's token expiry is read from the code host when the host is saved
or tested, a minute after start and then daily: GitLab reports it for personal, group and project
access tokens (`/api/v4/personal_access_tokens/self`), GitHub for fine-grained and expiring tokens.
GitHub Apps mint their own short-lived tokens, and Bitbucket doesn't report an expiry. The Code hosts
page shows each token's expiry.

When a license expires, paid features keep working for its grace period (14 days unless
the license says otherwise), with a banner in the admin panel. After that the console goes back
to the free core. It never stops reviewing, and settings for paid features are kept for when the
license is renewed.

## Environment

The console needs no config file. Its startup settings are environment variables, listed by
`code-review-console env` and in [examples/console.env](../examples/console.env); each one wins over
the same key in an optional config file.

| Variable | Config file key | |
|---|---|---|
| `CODEREVIEW_CONSOLE_ADDR` | `server.addr` | listen address, default `:8080` |
| `CODEREVIEW_CONSOLE_TRUSTED_PROXIES` | `server.trusted_proxies` | the load balancer's or ingress controller's own IPs, comma-separated; see [Client addresses](#client-addresses) |
| `CODEREVIEW_CONSOLE_TRUSTED_PROXY_HOPS` | `server.trusted_proxy_hops` | instead: how many proxies are in front (usually `1`) |
| `CODEREVIEW_CONSOLE_BASE_URL` | `admin.base_url` | public URL (sign-in redirect, webhook URLs) |
| `CODEREVIEW_CONSOLE_ADMIN_ENABLED` | `admin.enabled` | default true once an OIDC issuer is set |
| `CODEREVIEW_CONSOLE_SESSION_TTL` | `admin.session_ttl` | default `12h` |
| `CODEREVIEW_CONSOLE_COOKIE_SECRET` | `admin.cookie_secret` | 32+ random characters, for several replicas |
| `CODEREVIEW_CONSOLE_HSTS` | `admin.hsts.enabled` | default `true`: send `Strict-Transport-Security` when `base_url` is https; see [HTTPS](#https) |
| `CODEREVIEW_CONSOLE_HSTS_MAX_AGE` | `admin.hsts.max_age` | default `8760h` (a year) |
| `CODEREVIEW_CONSOLE_HSTS_INCLUDE_SUBDOMAINS` | `admin.hsts.include_subdomains` | default `false` |
| `CODEREVIEW_CONSOLE_OIDC_ISSUER`, `_OIDC_CLIENT_ID`, `_OIDC_CLIENT_SECRET` | `admin.oidc.*` | [admin.md](admin.md) |
| `CODEREVIEW_CONSOLE_OIDC_SCOPES`, `_OIDC_ALLOWED_EMAILS`, `_OIDC_ALLOWED_DOMAINS`, `_OIDC_ALLOWED_GROUPS` | `admin.oidc.*` | comma-separated; the allowed ones are admins |
| `CODEREVIEW_CONSOLE_OIDC_OPERATOR_EMAILS`, `_OIDC_OPERATOR_GROUPS`, `_OIDC_VIEWER_EMAILS`, `_OIDC_VIEWER_GROUPS` | `admin.oidc.*` | operators and read-only viewers ([roles](admin.md#roles)); license: `sso_rules` |
| `CODEREVIEW_CONSOLE_OIDC_GROUPS_CLAIM` | `admin.oidc.groups_claim` | default `groups` |
| `CODEREVIEW_CONSOLE_OIDC_ALLOWED_SUBJECTS`, `_OIDC_OPERATOR_SUBJECTS`, `_OIDC_VIEWER_SUBJECTS` | `admin.oidc.*_subjects` | match `subject_claim`; operators and viewers need `sso_rules` |
| `CODEREVIEW_CONSOLE_OIDC_SUBJECT_CLAIM` | `admin.oidc.subject_claim` | default `sub`; Entra ID: `oid` |
| `CODEREVIEW_CONSOLE_OIDC_REQUIRE_EMAIL_VERIFIED` | `admin.oidc.require_email_verified` | default `true`: email and domain rules need `email_verified: true` |
| `CODEREVIEW_CONSOLE_EXECUTOR` | `executor.type` | `local` (default), `docker` or `kubernetes` |
| `CODEREVIEW_CONSOLE_IMAGE` | `executor.docker.image`, `executor.kubernetes.image` | review image; its tag is used until a version is saved on the Executor page |
| `CODEREVIEW_CONSOLE_CLI_BINARY`, `_DOCKER_BINARY`, `_DOCKER_NETWORK` | `executor.local.binary`, `executor.docker.*` | |
| `CODEREVIEW_CONSOLE_KUBE_NAMESPACE`, `_KUBE_SERVICE_ACCOUNT`, `_KUBE_AUTOMOUNT_TOKEN`, `_KUBECONFIG` | `executor.kubernetes.*` | see [Kubernetes](#kubernetes) |
| `CODEREVIEW_CONSOLE_CONFIG` | | path of an optional config file |

Variables starting with `CODEREVIEW_` can't be passed to review jobs, so the OIDC client secret and
the other startup secrets stay in the console.

Secrets and credentials:

| Variable | Needed for |
|---|---|
| `DATABASE_URL` | **Required.** PostgreSQL 13+ (`database.url`): code hosts, models, settings, the license, history and learnings live there. Migrations run at startup under an advisory lock, so several replicas can start together. Replicas share webhook replay protection through it: a delivery is processed once, whichever replica receives it. |
| `CODEREVIEW_CONSOLE_LICENSE` | The Divmora license key (see [License](#license)) |
| `CODEREVIEW_CONSOLE_SECRET_KEY` | **Required.** 32 bytes as base64 or hex (`openssl rand -base64 32`), or a passphrase of 32+ characters. Host tokens and webhook secrets are encrypted with it, and each review job gets a key derived from it per code host (`CODEREVIEW_STATE_KEY`), which the CLI (0.6 or later) uses to sign what it posts, so its earlier comments still count after a host's token changes to another user. Changing this key only means older comments are no longer recognised as signed. **Keep it safe:** if it is lost, the stored tokens cannot be read and must be entered again. |
| `CODEREVIEW_CONSOLE_REGISTRY_USERNAME`, `CODEREVIEW_CONSOLE_REGISTRY_PASSWORD` | Optional: read-only credentials for a private image registry, so the **Executor** page can list the versions in your repository and pin them to their digests (public ones such as ghcr.io need none). Without them, or offline, you type the version. Amazon ECR's 12-hour passwords aren't refreshed, so type the version there. |
| Provider keys (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `AWS_*`, ...) | Forwarded to each review job; add other names under **Settings → Bot and review jobs** (the console's own secrets can't be added) |

`GET /healthz` returns `{"status":"ok"}` for probes. It no longer shows the version or the queue to
anyone who can reach the console: admins see both in the panel, and monitoring reads them from
[`/metrics`](#prometheus-metrics) (`codereview_build_info`, `codereview_queue_pending`,
`codereview_queue_running`).

## Docker Compose

```bash
cp examples/console.env .env      # set CODEREVIEW_CONSOLE_SECRET_KEY and the OIDC settings
docker compose up -d
docker compose exec console code-review-console check-config
```

Then sign in to the admin panel and follow **Setup**: add a code host, a model profile and the
license there.

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

Configure the Deployment with environment variables, for example:

```yaml
env:
  - { name: CODEREVIEW_CONSOLE_EXECUTOR, value: kubernetes }
  - { name: CODEREVIEW_CONSOLE_IMAGE, value: ghcr.io/divmora/code-review-bot:0.6.1 }
  - { name: CODEREVIEW_CONSOLE_KUBE_NAMESPACE, value: code-review }
  - { name: CODEREVIEW_CONSOLE_BASE_URL, value: https://review.example.com }
  - { name: CODEREVIEW_CONSOLE_OIDC_ISSUER, value: https://idp.example.com }
envFrom:
  - secretRef: { name: code-review-console }   # DATABASE_URL, CODEREVIEW_CONSOLE_SECRET_KEY,
                                               # CODEREVIEW_CONSOLE_OIDC_CLIENT_ID/_SECRET, model keys
```

Bind the Role to the console's service account with a RoleBinding. `patch` on secrets lets the console
make each job's Secret owned by its Job, so Kubernetes deletes the Secret with the Job even if the
console stops mid-review. Without it the console logs a warning and the Secret, which holds the job's
tokens and provider keys, can be left behind. `list` on events is optional: with it, a review pod that
Kubernetes refuses to create (a ResourceQuota, a LimitRange, an admission webhook) fails the review
at once with the reason; without it, the review waits for its deadline.

Leave `CODEREVIEW_CONSOLE_KUBECONFIG` (`executor.kubernetes.kubeconfig`) empty to use the in-cluster
config. Set `CODEREVIEW_CONSOLE_KUBE_SERVICE_ACCOUNT` (`executor.kubernetes.service_account`) only when review jobs need a cloud identity (for example IRSA,
EKS Pod Identity or GKE Workload Identity for Bedrock or Vertex). These don't need the Kubernetes API
token, so review pods never get one by default, with or without a service account. Set
`CODEREVIEW_CONSOLE_KUBE_AUTOMOUNT_TOKEN=true` only if your setup really needs the API
token in review pods. Review pods run untrusted PR content, so keep RBAC bound to that service account
to a minimum.

### Settings in the admin panel

The **Executor** page manages the image version (a tag, a digest, or both as `0.6.1@sha256:…`;
the repository stays in the config file), CPU and memory (1 CPU and 1Gi to start), and for
Kubernetes the disk (ephemeral storage, 2Gi by default) and /tmp size (1Gi by default), the job TTL,
node selector, tolerations, affinity, priority class, pod labels and annotations, and the CA bundle
below. Until a version is saved there, reviews use the image tag in the config file. The version is
picked from the release tags found in the image repository (your own registry or mirror works: the
console lists its tags with the registry API), or typed. A change applies to the next review without
a restart.

A version saved as a tag alone is **pinned to its digest**: the console asks the registry which image
the tag points to and saves `0.6.1@sha256:…`, so docker and Kubernetes pull exactly that image even if
the tag is re-pushed later, and the audit log records the digest. If the tag later points to another
image, the Executor page says so; reviews keep running the saved image until you choose **Pin the new
image**. When the registry can't be reached, the tag is saved unpinned and the page says why; save
again later to pin it. Typing a digest (or `tag@digest`) yourself keeps it as typed.

The executor type, binaries, the Docker network, `kubeconfig`, `namespace`, `service_account` and
`automount_service_account_token` stay in the config file only: they decide what review jobs, which
carry the code host tokens and model keys, can reach.

### Where review pods run

The **Executor** page's scheduling fields are copied into each review Job's pod template: node
selector, tolerations, affinity, priority class, pod labels and annotations. Tolerations and
affinity are JSON with the Kubernetes field names, for example this affinity:

```json
{"nodeAffinity": {"requiredDuringSchedulingIgnoredDuringExecution": {"nodeSelectorTerms": [
  {"matchExpressions": [{"key": "kubernetes.io/arch", "operator": "In", "values": ["amd64"]}]}]}}}
```

An unknown field is refused when you save, so a typo doesn't silently drop a rule. The priority
class must exist.
`pod_labels` are merged with the console's own labels, which win on conflict because the console
finds review pods by them. With Karpenter, `karpenter.sh/do-not-disrupt: "true"` keeps
consolidation from evicting a review mid-run.

### Private CA certificates

When a self-managed GitLab or GitHub Enterprise, an LLM gateway or a TLS-inspecting proxy uses a
certificate from a private CA, put the CA certificates (PEM) in a ConfigMap in the jobs'
namespace and name it under **CA bundle** on the Executor page (the key defaults to `ca.crt`). CA
certificates aren't secret, so the Executor page takes a ConfigMap. A Secret can only be the one the
config file names (`executor.kubernetes.ca_bundle.secret`, with its key): review pods process untrusted
code, and the namespace usually also holds the console's own Secret, which must never be mounted into
them. A Secret saved on the Executor page before 0.14.1 that isn't the config file's is no longer
mounted, and the console logs a warning at start. Create the ConfigMap like this:

```bash
kubectl -n code-review create configmap corporate-ca --from-file=ca.crt=corporate-ca.pem
```

The key is mounted read-only at `/etc/codereview/ca/ca.crt`, and `SSL_CERT_DIR` points at that
directory. These certificates are trusted **in addition to** the image's public CAs, so the bundle
only needs your own CAs. No extra RBAC is needed: the kubelet mounts the volume, not the console.

Point the Deployment's liveness and readiness probes at `GET /healthz` on the server port.

### Review pods' network access

Review pods hold the code host token (in the job's Secret) and the model credentials, and they read
untrusted input: diffs, files and comments from any merge request in an enabled repository. The CLI
has no tool that fetches URLs, but limiting what the pods can reach narrows the impact of any future
bug in it, a dependency or a provider SDK. Most clusters let pods reach everything by default.

A review pod needs, over HTTPS (443):

- the **code host API** (`gitlab.com` or your GitLab, `api.github.com` or your GitHub Enterprise,
  `api.bitbucket.org`);
- the **model endpoint**: Bedrock (`bedrock-runtime.<region>.amazonaws.com`), Anthropic, OpenAI,
  Gemini, your LLM gateway…;
- for IRSA or Workload Identity, the token exchange (`sts.<region>.amazonaws.com`,
  `sts.googleapis.com`);

plus **DNS**. It does **not** need the Kubernetes API (it gets no API token), the cloud metadata
endpoint (`169.254.169.254`), other services in the cluster or the internal network, or the image
registry (the kubelet pulls the image, not the pod). EKS Pod Identity is the exception: its agent
listens on `169.254.170.23:80`, so allow that address if you use it.

Every review pod carries the label `codereview/role: review` (the console never does), for a policy
to select. A standard `NetworkPolicy` can't match host names, so this one allows HTTPS to public
addresses only and blocks the metadata endpoint and private ranges:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: codereview-review-pods, namespace: code-review }
spec:
  podSelector:
    matchLabels: { codereview/role: review }
  policyTypes: [Ingress, Egress]
  ingress: []                      # nothing connects to a review pod
  egress:
    - to:                          # DNS
        - namespaceSelector: {}
          podSelector: { matchLabels: { k8s-app: kube-dns } }
      ports: [{ port: 53, protocol: UDP }, { port: 53, protocol: TCP }]
    - to:                          # HTTPS to public addresses
        - ipBlock:
            cidr: 0.0.0.0/0
            except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16]
      ports: [{ port: 443, protocol: TCP }]
    # A self-managed GitLab or an LLM gateway on a private address: allow it alone, e.g.
    # - to: [{ ipBlock: { cidr: 10.20.30.40/32 } }]
    #   ports: [{ port: 443, protocol: TCP }]
    # EKS Pod Identity:
    # - to: [{ ipBlock: { cidr: 169.254.170.23/32 } }]
    #   ports: [{ port: 80, protocol: TCP }]
```

The policy only takes effect with a network plugin that enforces it (Calico, Cilium, the AWS VPC CNI
with network policies turned on, GKE Dataplane V2…). With Cilium you can allow the exact host names
instead of all public addresses:

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: { name: codereview-review-pods, namespace: code-review }
spec:
  endpointSelector:
    matchLabels: { codereview/role: review }
  ingress: []
  egress:
    - toEndpoints:
        - matchLabels: { "k8s:io.kubernetes.pod.namespace": kube-system, k8s-app: kube-dns }
      toPorts:
        - ports: [{ port: "53", protocol: ANY }]
          rules: { dns: [{ matchPattern: "*" }] }
    - toFQDNs:
        - matchName: gitlab.example.com
        - matchName: bedrock-runtime.us-east-1.amazonaws.com
        - matchName: sts.us-east-1.amazonaws.com
      toPorts:
        - ports: [{ port: "443", protocol: TCP }]
```

Calico Enterprise and Calico Cloud have the same with `destination.domains` in a `NetworkPolicy`.

With the **Docker** executor, run review containers on a network of their own
(`CODEREVIEW_CONSOLE_DOCKER_NETWORK`) and limit it with the host's firewall the same way. On EC2, set
the instance's metadata hop limit to 1 so containers can't reach `169.254.169.254`.

## Config file and admin panel

Outside the panel are only what the console needs to start, and the limits a panel admin must not
change: the database, admin sign-in, the listen address, and the executor (type, image repository,
Kubernetes namespace, service account, token automount, kubeconfig, Docker network). Set them as
[environment variables](#environment) or, if you prefer, in a config file
([examples/console.yaml](../examples/console.yaml)).

Everything else is managed in the admin panel and stored in the database: code hosts and webhooks,
model profiles and the default model, the repository settings, retention and status page, the
executor's version, resources and scheduling, the license, and under **Settings → Queue**, **Review
engine** and **Bot and review jobs**: how many reviews run at once, the queue size, the review
timeout, the wait after a push, the codereview CLI's review settings (passes, verification, batch
size, repository defaults…), whether repositories may pick a model, the default bot name, extra
environment variables for review jobs, and who can open the status dashboard (see below). These apply without
a restart; another replica picks them up within 30 seconds. Code hosts and webhooks (added, changed,
rotated or deleted in the panel or with `console hosts import`) apply on every replica within 5
seconds, and a delivery to a webhook URL a replica doesn't know yet makes it check the database
first, so a webhook created a moment ago works on its first delivery.

**Upgrading from an older config file.** These keys used to be in the file: `github`, `gitlab`,
`bitbucket`, `models`, `model`, `fallback`, `repos`, `license`, `server.review_retention`,
`server.webhook_retention`, `server.workers`, `queue_size`, `job_timeout`, `debounce`,
`server.dashboard`, `review`, `allow_repo_model`, `bot_name`, `executor.env`, and the executor's
resources, TTL, scheduling, pod metadata and `ca_bundle`; also the `GITHUB_TOKEN`, `GITLAB_TOKEN`,
`CODEREVIEW_MODEL` and similar variables. On start, each one is copied into the database while the
database has no value for it yet, and ignored after that. The log lists the ones still present
(`these config file keys are managed in the admin panel now`): remove them once the panel shows
your setup. Model profiles with a literal `api_key` or with `headers` can't be stored in the panel:
put the key in an environment variable and add the profile under **Models** with that name as its
key variable. A `review:` key the panel doesn't know is reported in the log and not copied. A future
release stops reading these keys.

## Status dashboard

When it is on (**Settings → Status dashboard**), the dashboard shows every running, waiting and recent
review: repository, merge or pull request, errors, token use and the review's scores. Hiding
repository names hides the scores too. **Settings → Status dashboard access** decides who can open
it:

| Access | Who | Where |
|---|---|---|
| Signed-in admins only (default) | admins | `/admin/dashboard`, the sidebar link; `/dashboard` sends others to sign in |
| Admins, and anyone with the token | admins, and screens or people with the token | `/dashboard?token=…` or `Authorization: Bearer …` |
| Anyone who can reach the console | everyone | `/dashboard` |

The token is set or generated on the same page. Choose how long it works (7, 30 or 90 days, a year,
or never; 90 days by default) and copy the ready-made link before saving: only a SHA-256 hash of the
token is stored (in the database's settings, never in the audit log), so it can't be shown again.
The page shows when the token expires and warns a week before. An expired link tells its viewer to
ask an admin for a new one; generating a new token starts a new period and stops the old link.
An install that had a token before keeps working with it; one without a token is now for admins
only, until you choose otherwise. When the dashboard is public, consider hiding repository names.

## HTTPS

Serve the console over HTTPS, usually by terminating TLS at the load balancer or ingress. When
`base_url` starts with `https://`, every response carries
`Strict-Transport-Security: max-age=31536000`, so browsers that have visited once keep to HTTPS
for a year, even if someone later follows an `http://` link or a network tries to downgrade the
connection. Browsers ignore the header on plain-HTTP responses, so the redirect from HTTP to
HTTPS belongs to the load balancer.

- `admin.hsts.max_age` (`CODEREVIEW_CONSOLE_HSTS_MAX_AGE`) changes how long, default `8760h`.
- `admin.hsts.include_subdomains: true` extends it to every subdomain of the console's host. Set it
  only when they all serve HTTPS.
- `admin.hsts.enabled: false` (`CODEREVIEW_CONSOLE_HSTS=false`) stops sending it. Browsers remember
  the last value they saw, so to move the console back to HTTP, send a short `max_age` (such as
  `1s`) for a while first.

## Client addresses

Sign-ins, denied sign-ins, sign-outs and every audited change record the client's IP address and
browser (user agent); **Settings → Signed-in admins** shows each session's address, browser and last
activity; and the **Webhooks** page shows where each delivery, rejected ones included, came from.

Behind a load balancer or ingress, the connection comes from the proxy, so the console has to read
the client from `X-Forwarded-For`, a header anyone can send. Tell it which part to believe, in one
of two ways:

- **Hop count (recommended):** `CODEREVIEW_CONSOLE_TRUSTED_PROXY_HOPS=1` (`server.trusted_proxy_hops`)
  when exactly one proxy is in front, such as an AWS ALB, a Google or Azure load balancer, or one
  ingress controller. The console takes the address that proxy appended, which the client can't
  forge, and it keeps working when the proxy's addresses change. Use `2` for two proxies in a row
  (for example a CDN in front of the load balancer). A request that reaches the console without going
  through the proxies is recorded with its own address.
- **Trusted addresses:** `CODEREVIEW_CONSOLE_TRUSTED_PROXIES` (`server.trusted_proxies`) set to the
  proxies' own IP addresses. The console takes the right-most `X-Forwarded-For` address that isn't
  one of them.

> **List only the proxies' own addresses, never a range that clients connect from.** Any client
> that connects from a trusted address can choose the IP the console records in the audit log,
> sessions and webhook history. Ranges such as `10.0.0.0/8`, a pod network, or the load balancer's
> whole subnets usually include clients too: people on a VPN or zero-trust connector that SNATs into
> the network (Cloudflare WARP with `cloudflared`, Tailscale subnet routers, AWS Client VPN), bastions
> and other workloads. The console warns at startup when a trusted range is wider than /16 (IPv4) or
> /48 (IPv6). When the proxy's addresses aren't fixed, use the hop count.

Finding the addresses:

- **AWS ALB (target type ip):** its network interfaces,
  ```bash
  aws ec2 describe-network-interfaces --filters Name=description,Values='ELB app/<lb-name>/*' \
    --query 'NetworkInterfaces[].PrivateIpAddress' --output text
  ```
  They change when the ALB scales, so the hop count suits an ALB better.
- **ingress-nginx and similar controllers:** the controller pods' addresses (they change on
  restarts too), or the hop count.

Without either setting, the header is ignored and the connection's address is recorded, which behind
a proxy is the proxy itself.

## Prometheus metrics

`/metrics` serves the console's metrics in the Prometheus text format, which Prometheus,
VictoriaMetrics, Grafana Agent or Alloy, the OpenTelemetry collector and Datadog can scrape. It is off
until you choose who can read it under **Settings → Prometheus metrics**: scrapers with a bearer
token (generated there, stored hashed, shown once) or anyone who can reach the console. The page
gives a ready-made scrape config:

```yaml
- job_name: code-review-console
  scheme: https
  metrics_path: /metrics
  static_configs:
    - targets: ["review.example.com"]
  authorization:
    credentials: <the metrics token>
```

| Metric | Type | Labels |
|---|---|---|
| `codereview_build_info` | gauge | `version` |
| `codereview_start_time_seconds` | gauge | |
| `codereview_queue_pending`, `codereview_queue_running`, `codereview_queue_capacity`, `codereview_workers` | gauge | |
| `codereview_jobs_total` | counter | `kind` (review, full_review, answer, help), `state` (done, skipped, failed) |
| `codereview_job_duration_seconds` | histogram | `kind` |
| `codereview_model_tokens_total` | counter | `direction` (input, output) |
| `codereview_review_comments_total` | counter | |
| `codereview_webhooks_total` | counter | `platform`, `outcome` (queued, ignored, duplicate, rejected, error) |
| `codereview_license_state` | gauge | `state` (none, active, grace, expired, invalid) |
| `codereview_license_expiry_timestamp_seconds` | gauge | |
| `codereview_host_token_expiry_timestamp_seconds` | gauge | `platform`, `host`, `credential` (a credential's pattern; empty for the host's own token) |
| `codereview_review_score` | gauge | `kind` (quality, security, maintainability), `platform`, `host`, `repo` |

Counters and the queue are per replica (Prometheus adds them up), and their labels never carry
repository names. `codereview_review_score` is each repository's latest review score, read from the
database (the same on every replica, refreshed at most once a minute), one series per repository and
kind. While the status dashboard hides repository names, it has only the `kind` label instead: the
average of the repositories' latest scores. For example, to alert when a repository's latest
security score drops below 50: `codereview_review_score{kind="security"} < 50`.
For example, to alert two weeks before a code host token expires:

```yaml
- alert: CodeReviewHostTokenExpiring
  expr: codereview_host_token_expiry_timestamp_seconds - time() < 14 * 86400
```

## Moving code hosts

Code hosts and their webhooks are managed in the admin panel.

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
written, and unknown fields are rejected. Imports are audited as `cli: hosts import`. A running console
applies the import within 5 seconds, without a restart (versions before 0.12 need a restart).

## Upgrading

1. Read the release notes for a new minimum CLI version.
2. Update the image or binary. For `docker` or `kubernetes` executors, change the CLI version on the
   **Executor** page.
3. Run `check-config`.
4. Restart. Database migrations apply automatically. On SIGTERM the console stops taking work and
   gives running reviews up to 2 minutes to finish; give the container at least that long
   (Kubernetes `terminationGracePeriodSeconds: 150`). Reviews, answers and help replies
   that are still waiting (including pushes inside the wait-after-push window) are saved in the
   database and queued again when the console is back, with the wait they had left. A review that
   was running when the console stopped isn't run again on its own (it could post its comments
   twice): it shows on **Reviews** as interrupted, with **Re-run**. With several replicas, a replica
   that stops hands its waiting jobs to another (at once on a clean shutdown, within 90 seconds if it
   was killed), and each job runs once. Learnings saved from replies (`@bot remember`, replies to the
   bot) are not kept across a restart: ask again.

**Upgrading to 0.12: the shared webhook URLs are gone.** `/webhooks/github`, `/webhooks/gitlab` and
`/webhooks/bitbucket` no longer exist; a hook still pointing at one gets `404 Not Found` and is not in
the webhook history. Every hook needs a webhook of its own in the console (**Code hosts** → **Manage
webhooks** → **Add webhook**), with its own URL and secret. Before upgrading, check the **Webhooks**
page: deliveries listed as "shared URL" come from hooks to move. On the first start, 0.12 deletes the
webhook secrets and signing tokens that hosts kept for the shared URL (the log says which hosts), and
the config file's `webhook_secret`, `webhook_secrets`, `signing_token` and `signing_tokens` keys, and
`GITHUB_WEBHOOK_SECRET`, `GITLAB_WEBHOOK_SECRET` and `GITLAB_WEBHOOK_SIGNING_TOKEN`, are no longer
read. A hosts file exported from an older version still imports; its host-level webhook secrets are
ignored.
