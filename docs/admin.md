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
| Microsoft Entra ID | `https://login.microsoftonline.com/<tenant-id>/v2.0` | Enable *groups claim* in Token configuration. Group values are object IDs unless you configure names. Entra ID sends no `email_verified` claim, and its `email` can be set by users in some tenants: use `allowed_groups`, or `allowed_subjects` with `CODEREVIEW_CONSOLE_OIDC_SUBJECT_CLAIM=oid` (the user's object ID), rather than email rules. |
| AWS IAM Identity Center | via a customer-managed OIDC app, or front it with Keycloak/Cognito | Identity Center is a SAML IdP for most apps. |
| GitLab (as IdP) | `https://gitlab.example.com` | Applications → new app with `openid email profile`. Groups appear in the `groups_direct` claim: set `CODEREVIEW_CONSOLE_OIDC_GROUPS_CLAIM=groups_direct`. |

**Email rules need a verified email.** `allowed_emails`, `allowed_domains` and the operator and viewer email lists match only when the ID token says `email_verified: true` (`admin.oidc.require_email_verified`, default true). A provider that leaves the claim out may let a user set any address, including an admin's. There, use group rules, or `allowed_subjects`, `operator_subjects` and `viewer_subjects`, which match `admin.oidc.subject_claim` (default `sub`), an identifier the user can't change. Set `require_email_verified: false` only for a provider you know verifies every email it sends. A sign-in refused for this says so, in the audit log as `login.denied`.

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

**Change requests.** An operator's sensitive change doesn't apply: the console stores the request,
encrypted with `CODEREVIEW_CONSOLE_SECRET_KEY`, and lists it under **Change requests** (called
Approvals before 0.14; `/admin/approvals` redirects there) with secret values
masked. An admin approves it, which runs the change as the operator through the same checks, or
rejects it with a reason; the operator can cancel their own. The audit log records the request, the
decision and the change itself as `operator@example.com (approved by admin@example.com)`. Requests
expire after 7 days. Approving claims the request first, so it applies once even when two admins approve at the same time, and it can't be cancelled or rejected while it's being applied (it shows as *applying*); a request left applying because the console stopped is marked failed after 10 minutes, with a note to check whether the change took effect. When an approved change creates a webhook or a new secret, the secret is shown
once to the approving admin.

## 3. Use it

1. Open `https://<base_url>/admin` and sign in. Until reviews can run, the console opens **Setup**: a checklist (license, code host, webhook, a verified delivery, model, an enabled repository, the first review) that is checked against what is configured right now, says what to do next, and shows why a step isn't done (for example the reason the last delivery was rejected).
2. Repositories show up (disabled) when they send their first webhook, or click **Add repository**.
3. Set a repository to **Enabled** to start reviewing it; hover its state to see why it is or isn't reviewed. To change several at once, tick them and choose **Enable**, **Disable** or **Inherit**. To onboard a whole group, add a rule for it instead (a row's **⋯** menu → **Create a rule for** the group, or the hint shown when several repositories in a group are enabled one by one): the rule covers new repositories too. The group filter shows one group's repositories. **Settings** sets the review profile, comment limit, minimum severity, model profile, auto review, drafts, the flow diagram and extra instructions. These override the repository's `.codereview.yaml`. **Flow diagram** (off, auto or always) adds a diagram of the changed flow to the walkthrough; auto draws one when a change spans several layers, and `@bot diagram` draws one on request from the last review (refused where the setting is off). Review details show what a diagram cost in tokens, and Impact counts the merge requests that got one. It needs code-review-bot 0.7.0 or later on the Executor. **Labels** pick the project's own labels for each merge request: **Suggest** adds a line to the walkthrough, **Apply** also sets them on the merge request (the bot only ever removes labels it set itself; Bitbucket pull requests have no labels, so there it suggests only). **Allowed labels** are patterns such as `type::*` or `backend` (`*` any run of characters, `?` one, case-insensitive); only labels the project already has are used, one per scope (`type::`), and labels a person set are respected. **Suggest only** patterns (for example `priority::*`) are shown, never set. **Pick from a repository's labels** reads a repository's labels with the bot's token (an operator action) and marks which ones the patterns allow; click a label or a scope to add it. A rule's or repository's labels win field by field over the defaults (a mode set on a repository keeps the defaults' patterns). Review details and the Reviews list show the labels picked (filled: set on the merge request), and Reviews filters by one (`label=type::bug`). Labels need code-review-bot 0.8.0 or later on the Executor.
4. **Models** holds the model profiles: provider, model and the name of the console's environment variable that holds the API key. The console never stores keys; it reads the variable and hands the key to each review job, and the page shows whether the variable is set. Choose the **Default model** and the fallbacks tried in order when it fails. Profiles from an older config file's `models:` are copied here once on start (see [deploy.md](deploy.md#config-file-and-admin-panel)). A new environment variable needs a console restart before it is seen. The variable must be a provider's own key variable (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, …) or one named `CODEREVIEW_MODEL_KEY_<NAME>`, since its value goes to every review job and on to the profile's endpoint: no other secret of the console (the OIDC client secret, AWS or database settings) can be sent as a model key. A profile saved earlier with another name gets no key: the page marks it **not passed on**, the console logs a warning at start, and renaming the variable and updating the profile fixes it. When a profile sends its key to a host that isn't the provider's own (an openai-compatible endpoint, a proxy), the page says where, and so does an operator's change request.
5. **Executor** shows where reviews run (from the config file, read-only) and manages the review image's version, resources and, for Kubernetes, scheduling, pod labels and annotations, the CA bundle and the job TTL. Changes apply to the next review. See [deploy.md](deploy.md#settings-in-the-admin-panel).
6. **Settings → Admin sessions → Sign out after inactivity** ends a session that has made no request for that long (60 minutes by default, 0 for never; the panel's own refreshes don't count), recorded as `session.ended` with reason `idle`. **Settings → Signed-in admins** lists every admin session. Sign one out, or **Sign out everyone else** (after offboarding someone, or if a session may be compromised). Every request is also checked against the current sign-in rules and license, so an admin removed from `allowed_emails`, `allowed_domains` or `allowed_groups` (after the restart that applies the change) is signed out at once. Group membership comes from the identity provider at sign-in: a group removed there applies when the session ends (`session_ttl`) or when you sign the session out here.
7. **Review scores.** Every review the CLI scores (quality, security, maintainability, 1 to 100) keeps its scores, whether or not score labels are on. **Reviews** shows them per review, banded like the labels (80 and up, 50 to 79, below 50), filters by them ("security below 60" searches all reviews) and sorts the loaded reviews by them. With the metrics feature, it also shows the period's average scores and a trend chart; **Repositories** shows each repository's latest scores with a 30-day sparkline, and a repository's **Settings** panel charts its scores over 30 days, 12 months or all its history. Reviews stored before the upgrade get their scores from the stored result. The filters, sort, chart period and the open review are kept in the address (for example `/admin/reviews?score_kind=security&score_below=60&sort=security`, with the same names as `/admin/api/reviews`), so a reload or Back keeps the view and a filtered view can be bookmarked or shared. A review that approved the merge request on the code host is marked **✓ approved** on Reviews and on the status dashboard (or **approval withdrawn** / **approval failed**), and Reviews filters by it (`approval=approved`); this is the bot approving merge requests, set under Settings, not operator change requests.
8. **Approve merge requests** (Settings) has the bot approve a merge request whose review passes, as its token's user (or the GitHub App). Choose which findings block approval (critical; major or critical, the default; minor or above; any finding; or none, to approve whatever it found), an optional lowest score for all three scores, and whether a later review that doesn't pass withdraws the bot's approval (on by default) or leaves it. New findings, ones beyond the comment limit and ones still open from an earlier review count; questions don't. A review that can't decide (partial, too large) of a new commit also withdraws the bot's approval when withdrawing is on (code-review-bot 0.6 or later), so an approval never carries over to code that wasn't fully reviewed; a re-review of the same commit leaves it. The console always tells the CLI which you chose. **Rules** turn approval on or off for a host, group or pattern (**Approve MRs**), and **Repositories** shows where it applies. A review's details show what happened ("approved", "withdrawn", "not approved"…), and a refusal from the code host shows on the review as "could not approve". It needs code-review-bot 0.5 or later (the CLI version on **Executor**). On GitLab the bot's user must be allowed to approve and can't approve its own merge requests; on GitHub, withdrawing dismisses the bot's review, which branch protection can restrict. A partial or too-large review leaves the approval as it is.
9. **Impact** shows what the console has done for this installation, from its own database only (nothing leaves it): merge requests reviewed and review runs, findings raised by severity, security findings, the median time to first review (from the webhook to the first review's comments, including the wait after a push), active repositories, comments posted and average scores. Questions aren't counted as findings. The last 30 days are free; with the metrics feature, choose 90 days, 12 months or all history, see the trends, findings by category, the top repositories by reviews and by findings, and **Export CSV** or **Export JSON** for reports (exports are audited as `impact.export`). Periods are whole UTC days or months. The numbers come from rollups kept for good, like the review charts; on upgrade they are filled in from the reviews still stored. **Approvals by the bot** counts merge requests the bot approved on the code host (a review that approved; one already approved isn't counted again), and is in the exports as `approvals_by_the_bot`. The numbers link to **Reviews** with the matching filters and period: merge requests reviewed, findings raised (`severity=any`), security findings (`category=security`), each severity and category, and each top repository. **Merge requests by label** counts the merge requests reviews labelled, one scope at a time (for example `type::` for merge requests by type, or `component::` for database changes), each linking to Reviews; the exports have it as `merge_requests_by_label`, next to `merge_requests_with_flow_diagram`. Reviews can also be filtered by finding severity and category directly; `/admin/api/reviews` takes `severity` (or `any`) and `category`, counted the way Impact counts them (questions aren't findings). How many findings were acted on (fixed, resolved or dismissed) isn't tracked yet and isn't shown. Periods are whole UTC days or months; the label ends on the last day (or month) included, and the API and exports give that day as `through` next to the exclusive end (`to`, `to_exclusive` in the CSV).   
10. **Webhooks** shows every delivery. A delivery whose body was already received (a retry, an event arriving through two GitLab hooks, a replay) shows as a duplicate and is not processed again, for 7 days. Redelivering an old webhook from the code host therefore does nothing; use **Review now** on the Repositories page instead. The project and outcome filters are kept in the address.
11. **Audit log** lists sign-ins, denied sign-ins, sign-outs and every change, newest first, with who made it and from which address and browser. Each change shows only the fields that changed (old → new), with the raw entry one click away; secret changes are named without their values (`token:changed`, `signing_tokens:+1`). Filter by who, action (`host.*` matches every host action), target, a text search and dates; **Older entries** pages back through everything, and **Export CSV** or **Export JSON Lines** downloads the filtered view (up to 100,000 entries; exports are audited too). Times are shown as `23 Nov 2026, 09:33:38 IST` in your browser's time zone, with UTC on hover. The applied filter is kept in the address (`/admin/audit?action=host.*`).
12. **Unsaved changes.** On **Settings**, edited cards are marked **Unsaved**, and a bar at the bottom of the window lists what isn't saved yet (the settings, the default review settings, the queue and review settings) with **Save** and **Discard**. Leaving the page or following a link asks first. If the session ended before you save, the changes stay on the page: sign in again in the new tab it offers, come back and save.

With the allowlist on (**Settings**), webhooks and `@codereview` commands from disabled repositories are ignored. If the database is unreachable, reviews are refused rather than run unchecked.
