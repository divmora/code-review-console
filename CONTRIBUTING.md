# Contributing

## Prerequisites

Go 1.27+, Docker (for `make test-db` and `make test-e2e`), and a checkout of
[code-review-bot](https://github.com/divmora/code-review-bot) next to this repo until it is published.

## Workflow

```bash
make build
make test
make test-db
make test-e2e
```

Use [Conventional Commits](https://www.conventionalcommits.org/) for PR titles. Read [AGENTS.md](AGENTS.md) for the
executor contract and the invariants every change must keep. Contributions are licensed under the
Elastic License 2.0 in [LICENSE](LICENSE). Before your first pull request is merged, sign the
[Contributor License Agreement](CLA.md): the CLA bot asks on the pull request, and you sign by replying
`I have read the CLA Document and I hereby sign the CLA`.
