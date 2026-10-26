# Testing

## Layers (all required for a new or migrated resource)
1. **Unit.** Expand/flatten, normalisers, validators, ID parsing, error mapping, name resolution.
   Table-driven. Run with `-race` in CI.
2. **Client contract.** An `httptest` server replays recorded MTN Cloud responses from
   `internal/client/testdata/<area>/` (scrubbed). Covers retries, 404/409/429, pagination, polling
   and context cancellation.
3. **Provider unit.** `resource.UnitTest` against the mock server: plan → apply → empty plan → import.
4. **Acceptance** (`TF_ACC=1`, real MTN Cloud, test tenant only, explicit permission). Per resource:
   - `_basic`: create, then a second step with `plancheck.ExpectEmptyPlan()`
   - `_update`: in-place changes, and `RequiresReplace` where expected
   - `_import`: `ImportStateVerify`
   - `_disappears`: deleted out of band, so the plan shows a recreate
   - `_drift`: changed out of band, so the plan shows the fix
   - `CheckDestroy`: confirms deletion through the API

Mutation check: a new test must fail when the behaviour it covers is broken. Break it once
deliberately and confirm the failure before trusting it.

## Repo-wide gates (CI, every PR)
| Gate | Command |
|---|---|
| Format | `golangci-lint fmt --diff` (gofmt + goimports) |
| Lint | `golangci-lint run` (the legacy baseline in `.golangci.yml` only shrinks) |
| Vet + tests | `go vet ./...`, `go test -race ./...` |
| Schema | `TestProvider_*` (InternalValidate, prefixes, removed types, names, env vars, sensitive) |
| Build | `go build ./...`, plus `goreleaser check` |
| Docs drift | `make docs`, then `git diff --exit-code docs/` |
| Name gate | `scripts/check-names.sh` |
| Examples | `terraform fmt -check -recursive examples` and `scripts/validate-examples.sh` |
| Vulnerabilities | `govulncheck ./...` |
| Terraform versions | examples validated with Terraform 1.5, latest, and OpenTofu latest |

Coverage target: at least 80% on `internal/client`, `internal/tenancy`, normalisers and expand/flatten.

## Acceptance CI
`.github/workflows/acceptance.yml`: manual dispatch only until the test tenant exists (Q6). It runs
the sweepers before and after, and reports in the run summary. It never runs for pull requests.
