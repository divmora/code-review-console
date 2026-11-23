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

The shared URL `https://<your-host>/webhooks/github` keeps working for existing apps, with the webhook secret in the host's settings.

Name the app after your bot name (default `codereview`) so `@codereview` mentions match. If the app slug differs, set the GitHub host's bot name (or the default under Settings) to the slug.

After creating it, note the **App ID** and generate a **private key** (`.pem`).

## 2. Add the host

In the console, **Code hosts** → **Add host** → GitHub: paste the App ID and the private key (stored encrypted). For GitHub Enterprise Server, set the API URL (`https://ghe.example.com/api/v3`).

## 3. Install the app

Install it on the repositories to review. The first push to an open PR, or opening a PR, triggers a review. Comment `@codereview help` on a PR to check it is listening.

## Token mode (no app)

For a quick trial, give the GitHub host a bot account's token instead of an App ID. Comments appear as that account, and the server ignores that account's own comments.
