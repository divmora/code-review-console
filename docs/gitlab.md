# GitLab setup

Works with gitlab.com and self-managed GitLab (15.7+ recommended; older versions fall back to the `/changes` API).

## 1. Bot identity and token

Create a dedicated user (for example `codereview`) or a **project/group access token** with the `api` scope and the *Developer* role. The bot comments as this identity, and the server ignores that identity's own notes.

## 2. Webhook

Project (or group) → Settings → Webhooks → **Add new webhook**:

| Field | Value |
|---|---|
| URL | `https://<your-host>/webhooks/gitlab` |
| Secret token | a long random string |
| Triggers | **Merge request events** · **Comments** |
| SSL verification | enabled |

Only pushes trigger reviews (`update` events with new commits). Title, label and description edits are ignored.

## 3. Server config

```yaml
bot_name: codereview          # mention handle: @codereview review
gitlab:
  - base_url: https://gitlab.example.com
    token: ${GITLAB_TOKEN}
    webhook_secret: ${GITLAB_WEBHOOK_SECRET}
```

Add one entry per instance. The server matches webhooks by the `X-Gitlab-Instance` header, then by the project URL host.

## CI instead of a server

The free [code-review-bot](https://github.com/divmora/code-review-bot) CLI can run in GitLab CI instead: see its `examples/gitlab-ci.yml`. `CI_JOB_TOKEN` cannot write MR notes, so provide `GITLAB_TOKEN` as a masked CI/CD variable.
