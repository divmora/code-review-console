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
executor contract and the invariants every change must keep. Contributions are licensed under
[LICENSE](LICENSE) (BSL 1.1), or [LICENSE-ENTERPRISE](LICENSE-ENTERPRISE) for the files that carry its header.
