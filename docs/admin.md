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
| GitLab (as IdP) | `https://gitlab.example.com` | Applications → new app with `openid email profile`. Groups appear in the `groups_direct` claim: set `groups_claim: groups_direct`. |

## 2. Configure the server

```yaml
database:
  url: ${DATABASE_URL}

repos:
  allowlist: true          # only review repositories enabled in the panel

admin:
  enabled: true
  base_url: https://review.example.com
  session_ttl: 12h
  oidc:
    issuer: https://example.okta.com
    client_id: ${OIDC_CLIENT_ID}
    client_secret: ${OIDC_CLIENT_SECRET}
    scopes: [openid, email, profile, groups]
    allowed_groups: [code-review-admins]
    # allowed_domains: [example.com]
    # allowed_emails: [lead@example.com]
```

Any one matching rule grants admin access. Email and domain rules only match when the provider doesn't mark the email unverified.

> **`allowed_domains` and `allowed_groups` need a license.** They are a paid feature (`sso_rules`). On the free core, or with a key that doesn't include `sso_rules`, only `allowed_emails` grants access, and the console logs a warning at startup when domain or group rules are configured. List at least one admin in `allowed_emails` so you can sign in and paste a key under **License**; domain and group rules start working as soon as a key with `sso_rules` is saved, with no restart.

When you run several instances behind a load balancer, also set `admin.cookie_secret` (32+ random characters) so a sign-in started on one instance can finish on another.

## 3. Use it

1. Open `https://<base_url>/admin` and sign in. Until reviews can run, the console opens **Setup**: a checklist (license, code host, webhook, a verified delivery, model, an enabled repository, the first review) that is checked against what is configured right now, says what to do next, and shows why a step isn't done (for example the reason the last delivery was rejected).
2. Repositories show up (disabled) when they send their first webhook, or click **Add repository**.
3. Toggle **Enabled** to start reviewing. **Settings** sets the review profile, comment limit, minimum severity, model profile, auto review, drafts and extra instructions. These override the repository's `.codereview.yaml`.
4. **Models** holds the model profiles: provider, model and the name of the console's environment variable that holds the API key. The console never stores keys; it reads the variable and hands the key to each review job, and the page shows whether the variable is set. Choose the **Default model** and the fallbacks tried in order when it fails. Profiles from the config file's `models:` are listed read-only (a console profile of the same name replaces one; **Copy to console** starts from it). A new environment variable needs a console restart before it is seen.
5. **Executor** shows where reviews run (from the config file, read-only) and manages the review image's version, resources and, for Kubernetes, scheduling, pod labels and annotations, the CA bundle and the job TTL. Changes apply to the next review. See [deploy.md](deploy.md#settings-in-the-admin-panel).
6. **Audit log** lists sign-ins, denied sign-ins and every change with before and after values.

With `repos.allowlist: true`, webhooks and `@codereview` commands from disabled repositories are ignored. If the database is unreachable, reviews are refused rather than run unchecked.
