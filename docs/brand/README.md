# Brand files

The Divmora Code Review mark: code brackets with a check, on Divmora's dark navy (`#0F172A`)
with blue (`#60A5FA`) and white.

| File | Use |
|---|---|
| `logo.svg` | The mark (README, website, docs). |
| `favicon.svg` | Heavier strokes for 16–32 px: browser tabs (admin panel, status dashboard, website). |
| `logo-512.png` | Avatar for the bot account on GitHub, GitLab and Bitbucket. |
| `logo-180.png` | Apple touch icon and other places that need a PNG. |
| `logo-32.png` | Small PNG for places that don't take SVG. |

The admin panel draws the same mark in `web/src/components/Logo.tsx` and serves
`web/public/assets/favicon.svg`. Change all of them together.
