# Admin panel setup

The admin panel (`/admin`) controls which repositories are reviewed, sets per-repository review settings, and keeps an audit log. Admins sign in through your identity provider using OpenID Connect.

## Requirements

- PostgreSQL 13+ (`database.url`). The schema is created and migrated automatically at startup.
- A public HTTPS URL for the server (`admin.base_url`).
- An OIDC client registered with your identity provider.

## 1. Register an OIDC client

Create a **web application** client with:

| Setting | Value |
|---|---|
| Redirect / callback URI | `https://<base_url>/admin/callback` |
| Grant type | Authorization code (PKCE is used automatically) |
| Scopes | `openid email profile` (plus `groups` if your provider needs it) |

Provider notes:

| Provider | `issuer` | Notes |
|---|---|---|
| Google Workspace | `https://accounts.google.com` | Google Cloud console → APIs & Services → Credentials → OAuth client (Web). Use `allowed_domains` for your Workspace domain. Google doesn't send groups. |
| Okta | `https://<org>.okta.com` or `https://<org>.okta.com/oauth2/default` | Add a `groups` claim to the ID token to use `allowed_groups`. |
| Keycloak | `https://<host>/realms/<realm>` | Add a *Group Membership* mapper (claim `groups`, full path off) to use groups. |
| Microsoft Entra ID | `https://login.microsoftonline.com/<tenant-id>/v2.0` | Enable *groups claim* in Token configuration. Group values are object IDs unless you configure names. |
| AWS IAM Identity Center | via a customer-managed OIDC app, or front it with Keycloak/Cognito | Identity Center is a SAML IdP for most apps. |
| GitLab (as IdP) | `https://gitlab.example.com` | Applications → new app with `openid email profile`. Groups appear in the `groups_direct` claim: set `CODEREVIEW_CONSOLE_OIDC_GROUPS_CLAIM=groups_direct`. |

## 2. Configure the server

With environment variables (no config file needed):

```bash
DATABASE_URL=postgres://...                 # required, with CODEREVIEW_CONSOLE_SECRET_KEY
CODEREVIEW_CONSOLE_BASE_URL=https://review.example.com
CODEREVIEW_CONSOLE_OIDC_ISSUER=https://example.okta.com
CODEREVIEW_CONSOLE_OIDC_CLIENT_ID=...
CODEREVIEW_CONSOLE_OIDC_CLIENT_SECRET=...
CODEREVIEW_CONSOLE_OIDC_SCOPES=openid,email,profile,groups
CODEREVIEW_CONSOLE_OIDC_ALLOWED_GROUPS=code-review-admins
# CODEREVIEW_CONSOLE_OIDC_ALLOWED_DOMAINS=example.com
# CODEREVIEW_CONSOLE_OIDC_ALLOWED_EMAILS=lead@example.com
# CODEREVIEW_CONSOLE_SESSION_TTL=12h
```

The admin panel turns on once an issuer is set. The same settings can go in an optional config file:

```yaml
admin:
  base_url: https://review.example.com
  session_ttl: 12h
  oidc:
    issuer: https://example.okta.com
    client_id: ${OIDC_CLIENT_ID}
    client_secret: ${OIDC_CLIENT_SECRET}
    scopes: [openid, email, profile, groups]
    allowed_groups: [code-review-admins]
```

Any one matching rule grants admin access. Email and domain rules only match when the provider doesn't mark the email unverified.

> **`allowed_domains` and `allowed_groups` need a license.** They are a paid feature (`sso_rules`). On the free core, or with a key that doesn't include `sso_rules`, only `allowed_emails` grants access, and the console logs a warning at startup when domain or group rules are configured. List at least one admin in `allowed_emails` so you can sign in and paste a key under **License**; domain and group rules start working as soon as a key with `sso_rules` is saved, with no restart.

When you run several instances behind a load balancer, also set `CODEREVIEW_CONSOLE_COOKIE_SECRET` (`admin.cookie_secret`, 32+ random characters) so a sign-in started on one instance can finish on another.

## Roles

Everyone who matches `allowed_emails`, `allowed_domains` or `allowed_groups` is an **admin**, as
before. With a license that has `sso_rules`, two more roles can sign in:

| Role | Can | Set with |
|---|---|---|
| Admin | everything; approves operators' changes | `allowed_emails`, `allowed_domains`, `allowed_groups` |
| Operator | see every page; change repositories, rules and learnings, run **Review now** and re-runs, test hosts. Changes to code hosts, webhooks, settings, models, the executor and the license are sent to an admin for approval | `CODEREVIEW_CONSOLE_OIDC_OPERATOR_EMAILS`, `..._OPERATOR_GROUPS` (`operator_emails`, `operator_groups`) |
| Viewer | see every page, the audit log and its export; change nothing | `CODEREVIEW_CONSOLE_OIDC_VIEWER_EMAILS`, `..._VIEWER_GROUPS` (`viewer_emails`, `viewer_groups`) |

Someone in several lists gets the strongest role. The role is worked out on every request from the
current rules, so moving someone between lists applies at once (after the restart that applies the
change; group membership comes from the identity provider at sign-in).

**Approvals.** An operator's sensitive change doesn't apply: the console stores the request,
encrypted with `CODEREVIEW_CONSOLE_SECRET_KEY`, and lists it under **Approvals** with secret values
masked. An admin approves it, which runs the change as the operator through the same checks, or
rejects it with a reason; the operator can cancel their own. The audit log records the request, the
decision and the change itself as `operator@example.com (approved by admin@example.com)`. Requests
expire after 7 days. When an approved change creates a webhook or a new secret, the secret is shown
once to the approving admin.

## 3. Use it

1. Open `https://<base_url>/admin` and sign in. Until reviews can run, the console opens **Setup**: a checklist (license, code host, webhook, a verified delivery, model, an enabled repository, the first review) that is checked against what is configured right now, says what to do next, and shows why a step isn't done (for example the reason the last delivery was rejected).
2. Repositories show up (disabled) when they send their first webhook, or click **Add repository**.
3. Toggle **Enabled** to start reviewing. **Settings** sets the review profile, comment limit, minimum severity, model profile, auto review, drafts and extra instructions. These override the repository's `.codereview.yaml`.
4. **Models** holds the model profiles: provider, model and the name of the console's environment variable that holds the API key. The console never stores keys; it reads the variable and hands the key to each review job, and the page shows whether the variable is set. Choose the **Default model** and the fallbacks tried in order when it fails. Profiles from an older config file's `models:` are copied here once on start (see [deploy.md](deploy.md#config-file-and-admin-panel)). A new environment variable needs a console restart before it is seen.
5. **Executor** shows where reviews run (from the config file, read-only) and manages the review image's version, resources and, for Kubernetes, scheduling, pod labels and annotations, the CA bundle and the job TTL. Changes apply to the next review. See [deploy.md](deploy.md#settings-in-the-admin-panel).
6. **Settings → Admin sessions → Sign out after inactivity** ends a session that has made no request for that long (60 minutes by default, 0 for never; the panel's own refreshes don't count), recorded as `session.ended` with reason `idle`. **Settings → Signed-in admins** lists every admin session. Sign one out, or **Sign out everyone else** (after offboarding someone, or if a session may be compromised). Every request is also checked against the current sign-in rules and license, so an admin removed from `allowed_emails`, `allowed_domains` or `allowed_groups` (after the restart that applies the change) is signed out at once. Group membership comes from the identity provider at sign-in: a group removed there applies when the session ends (`session_ttl`) or when you sign the session out here.
7. **Review scores.** Every review the CLI scores (quality, security, maintainability, 1 to 100) keeps its scores, whether or not score labels are on. **Reviews** shows them per review, banded like the labels (80 and up, 50 to 79, below 50), filters by them ("security below 60" searches all reviews) and sorts the loaded reviews by them. With the metrics feature, it also shows the period's average scores and a trend chart; **Repositories** shows each repository's latest scores with a 30-day sparkline, and a repository's **Settings** panel charts its scores over 30 days, 12 months or all its history. Reviews stored before the upgrade get their scores from the stored result.
8. **Approve merge requests** (Settings) has the bot approve a merge request whose review passes, as its token's user (or the GitHub App). Choose which findings block approval (critical; major or critical, the default; minor or above; any finding; or none, to approve whatever it found), an optional lowest score for all three scores, and whether a later review that doesn't pass withdraws the bot's approval (on by default) or leaves it. New findings, ones beyond the comment limit and ones still open from an earlier review count; questions don't. A partial or too-large review leaves the approval as it is. **Rules** turn approval on or off for a host, group or pattern (**Approve MRs**), and **Repositories** shows where it applies. A review's details show what happened ("approved", "withdrawn", "not approved"…), and a refusal from the code host shows on the review as "could not approve". It needs code-review-bot 0.5 or later (the CLI version on **Executor**). On GitLab the bot's user must be allowed to approve and can't approve its own merge requests; on GitHub, withdrawing dismisses the bot's review, which branch protection can restrict.
9. **Impact** shows what the console has done for this installation, from its own database only (nothing leaves it): merge requests reviewed and review runs, findings raised by severity, security findings, the median time to first review (from the webhook to the first review's comments, including the wait after a push), active repositories, comments posted and average scores. Questions aren't counted as findings. The last 30 days are free; with the metrics feature, choose 90 days, 12 months or all history, see the trends, findings by category, the top repositories by reviews and by findings, and **Export CSV** or **Export JSON** for reports (exports are audited as `impact.export`). Periods are whole UTC days or months; the label ends on the last day (or month) included, and the API and exports give that day as `through` next to the exclusive end (`to`, `to_exclusive` in the CSV). The numbers come from rollups kept for good, like the review charts; on upgrade they are filled in from the reviews still stored. How many findings were acted on (fixed, resolved or dismissed) isn't tracked yet and isn't shown.
10. **Webhooks** shows every delivery. A delivery whose body was already received (a retry, an event arriving through two GitLab hooks, a replay) shows as a duplicate and is not processed again, for 7 days. Redelivering an old webhook from the code host therefore does nothing; use **Review now** on the Repositories page instead.
11. **Audit log** lists sign-ins, denied sign-ins, sign-outs and every change, newest first, with who made it and from which address and browser. Each change shows only the fields that changed (old → new), with the raw entry one click away; secret changes are named without their values (`token:changed`, `signing_tokens:+1`). Filter by who, action (`host.*` matches every host action), target, a text search and dates; **Older entries** pages back through everything, and **Export CSV** or **Export JSON Lines** downloads the filtered view (up to 100,000 entries; exports are audited too). Times are shown as `23 Nov 2026, 09:33:38 IST` in your browser's time zone, with UTC on hover.

With the allowlist on (**Settings**), webhooks and `@codereview` commands from disabled repositories are ignored. If the database is unreachable, reviews are refused rather than run unchecked.
