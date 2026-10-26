---
name: ship-check
description: The gate before any terraform-provider-mtncloud work is reported as done or a PR is opened. Runs format, lint, vet, tests, build, docs drift, the name gate and example validation, and confirms the review happened. Use at the end of every resource or client change, or when the user asks "is it ready?".
---

# Ship check

Run every step and report each result honestly, with the command and a short excerpt of its output.
Stop and fix on the first failure, then re-run from the top.

1. **Static:** `golangci-lint fmt --diff` (no output), `go vet ./...`, `golangci-lint run ./...` (0 issues).
2. **Tests:** `go test ./...` (and `-race` on Linux/CI). Nothing skipped without a reason. New tests
   were mutation-checked: broken once on purpose, and they failed.
3. **Build:** `go build ./...`.
4. **Docs:** `make docs`, then `git status --porcelain docs/` is empty (or the regenerated docs are
   part of the change). `tfplugindocs validate` passes.
5. **Names:** `bash scripts/check-names.sh` prints OK.
6. **Examples:** `terraform fmt -check -recursive examples` and `bash scripts/validate-examples.sh`
   print OK.
7. **Secrets:** `git status` and `git diff --cached` show no `*.tfvars`, state, `.env`, tokens or real
   IDs/IPs. Fixtures are scrubbed.
8. **Review:** the `mtncloud-code-reviewer` agent ran on the diff, and every confirmed finding is
   fixed or answered.
9. **Acceptance (only if the owner approved a run):** the result, with the test tenant named.

Report: a table of steps with pass/fail, and then anything not verified, said plainly.
