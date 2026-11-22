# GitHub App setup

A GitHub App is the recommended way to run the console for GitHub. It gets its own bot identity, short-lived installation tokens, and per-repo installation.

## 1. Create the app

GitHub → Settings → Developer settings → GitHub Apps → **New GitHub App** (or the same page under your organization).

| Field | Value |
|---|---|
| Webhook URL | from the console: **Code hosts** → **Manage webhooks** → **Add webhook** gives `https://<your-host>/webhooks/github/<uuid>` |
| Webhook secret | the secret the console shows with it (once) |
| Repository permissions | **Pull requests**: Read and write · **Contents**: Read-only · **Metadata**: Read-only |
| Subscribe to events | **Pull request** · **Issue comment** |

Without a database (hosts from the config file), use the shared URL `https://<your-host>/webhooks/github` and a long random secret in `GITHUB_WEBHOOK_SECRET` instead. The shared URL keeps working for existing apps.

Name the app after your `bot_name` (default `codereview`) so `@codereview` mentions match. If the app slug differs, set `bot_name` to the slug.

After creating it, note the **App ID** and generate a **private key** (`.pem`).

## 2. Configure the server

```yaml
github:
  app_id: 123456
  private_key_file: /secrets/github-app.pem   # or private_key: "${GITHUB_APP_PRIVATE_KEY}"
  webhook_secret: ${GITHUB_WEBHOOK_SECRET}
  # GitHub Enterprise Server:
  # api_url: https://ghe.example.com/api/v3
```

## 3. Install the app

Install it on the repositories to review. The first push to an open PR, or opening a PR, triggers a review. Comment `@codereview help` on a PR to check it is listening.

## Token mode (no app)

For a quick trial, set `github.token` (or `GITHUB_TOKEN`) to a bot account's token instead of `app_id`. Comments appear as that account, and the server ignores that account's own comments.
