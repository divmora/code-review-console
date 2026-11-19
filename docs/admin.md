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

When you run several instances behind a load balancer, also set `admin.cookie_secret` (32+ random characters) so a sign-in started on one instance can finish on another.

## 3. Use it

1. Open `https://<base_url>/admin` and sign in.
2. Repositories show up (disabled) when they send their first webhook, or click **Add repository**.
3. Toggle **Enabled** to start reviewing. **Settings** sets the review profile, comment limit, minimum severity, model profile, auto review, drafts and extra instructions. These override the repository's `.codereview.yaml`.
4. **Audit log** lists sign-ins, denied sign-ins and every change with before and after values.

With `repos.allowlist: true`, webhooks and `@codereview` commands from disabled repositories are ignored. If the database is unreachable, reviews are refused rather than run unchecked.
