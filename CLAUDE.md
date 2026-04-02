# CLAUDE.md

## Project

Terraform provider for Salt (masterless mode via SSH). Go module: `github.com/bartei/terraform-provider-salt`.

## Pre-push checklist

Before committing and pushing, always run these checks and fix any issues:

```bash
gofmt -w .           # Fix formatting (CI will reject unformatted code)
go vet ./...         # Static analysis
go test ./... # Unit tests
go build ./...       # Ensure it compiles
```

The CI pipeline runs `gofmt -l .` and fails if any files need formatting. Always format before pushing.

## Commits

- Do not add Co-Authored-By lines
- Use conventional commits (feat:, fix:, docs:, ci:, chore:) — GoReleaser uses these for changelog generation

## Testing

- `make test` — unit tests only (fast, no VM needed)
- `make e2e` — full end-to-end tests against a QEMU VM (starts VM automatically)
- `make vm-start` / `make vm-stop` — manage the test VM manually

## Provider source

The provider source is `bartei/salt` (not stefanob). The GitHub org is `bartei`.
