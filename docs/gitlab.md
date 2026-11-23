# GitLab setup

Works with gitlab.com and self-managed GitLab (15.7+ recommended; older versions fall back to the `/changes` API).

## 1. Bot identity and token

Create a dedicated user (for example `codereview`) or a **project/group access token** with the `api` scope and the *Developer* role. The bot comments as this identity, and the server ignores that identity's own notes.

## 2. Webhook

In the console, open **Code hosts**, then **Manage webhooks** on the GitLab host, and click **Add webhook**. Name it after where you'll set it up (for example the group). The console generates:

- a URL of its own, `https://<your-host>/webhooks/gitlab/<uuid>`;
- a secret token, shown once.

Then in GitLab, Project (or group, or Admin → System hooks) → Settings → Webhooks → **Add new webhook**:

| Field | Value |
|---|---|
| URL | the webhook's URL from the console |
| Secret token | the secret from the console |
| Triggers | **Merge request events** · **Comments** |
| SSL verification | enabled |

Add one webhook in the console for each hook you set up in GitLab (a system hook, each group hook).

**A system hook alone is not enough.** GitLab system hooks send merge request events but never comments, so `@bot` commands and learnings from replies need a group or project webhook with **Comments**. A system hook plus group hooks is fine: the console drops the duplicate merge request event. When a host receives deliveries but no comment events, the Code hosts page and Setup say so. Each has its own URL and secret, so one can be given a new secret, disabled or deleted without touching the others. The URL alone picks the host, and only that webhook's secret verifies it.

**Signing tokens (GitLab 19.0+, recommended).** After saving the hook, GitLab shows its signing token (`whsec_…`). Paste it into the webhook in the console. Signed deliveries are then checked by signature, and unsigned ones are rejected: the older secret token no longer works on its own, so someone who has it (from an old hook or a screenshot) can't send events without a signature. While some hooks still send unsigned events, set the webhook's **Unsigned deliveries with the secret token** to **Accept too**; deliveries accepted that way show as "secret token, not signed" on the Webhooks page. Each webhook shows what it accepts. If the host's **Unsigned deliveries** setting is **Reject everywhere**, every webhook of the host needs its signing token.

Only pushes trigger reviews (`update` events with new commits). Title, label and description edits are ignored.

**Shared URL.** `https://<your-host>/webhooks/gitlab` still works for existing hooks. It is shared by every GitLab host: the console matches the instance by the `X-Gitlab-Instance` header, then by the project URL host, and checks the secret tokens or signing tokens in the host's settings. It never accepts a webhook's own secret. The webhook history (**Webhooks** page) shows which URL each delivery used, so you can move hooks over at your own pace.

## 3. Add the host

In the console, **Code hosts** → **Add host** → GitLab: the instance URL (`https://gitlab.example.com`) and the token from step 1. **Test** checks the token. Add one host per instance; each has its own token, webhooks and, optionally, its own bot name (the mention handle, `@codereview review`) and reviewer username.

## CI instead of a server

The free [code-review-bot](https://github.com/divmora/code-review-bot) CLI can run in GitLab CI instead: see its `examples/gitlab-ci.yml`. `CI_JOB_TOKEN` cannot write MR notes, so provide `GITLAB_TOKEN` as a masked CI/CD variable.
