# Workflow

## Always read the docs (owner's rule)
Before designing or changing anything, read both:
1. **MTN's own docs**: the Drive folder `V0.1_Documentations` (id `1RIYe5FMG-2P4vvcdJuZBjNIGM52zAj6t`;
   for example Project-Creation-Workflow, Network-and-Router-Creation-Workflow, Instance Access & Security)
   and the MTN-Cloud-Service-Automation repo. They define the customer flow and the prerequisites.
2. **The platform API via Context7** (`/websites/apidocs_morpheusdata_reference`). Never use memory.
Record what you read, and what it changed, in `dev-docs/`. If neither covers it, ask the owner for a
read-only CLI payload.

## Plan before code
- Propose the change first: files, schema (every attribute marked Required/Optional/Computed and why),
  API calls with their documentation source, tests, and docs. Wait for the owner's go-ahead before
  writing Go code. The owner has asked for this explicitly.
- Build every resource from documentation plus a real payload ([dev-docs/api-research.md](../../dev-docs/api-research.md)).
  Use Context7 (`/websites/apidocs_morpheusdata_reference`) whenever you are unsure, and never design
  from memory. If a payload is missing, ask the owner to run the read-only CLI command and name the
  exact command.
- Read the relevant MTN Drive doc or automation script before designing behaviour that depends on
  MTN's setup, and cite it in the PR.

## Branches and PRs
- Never commit to `main`. Work on `feat/<area>-<thing>`, `fix/...` or `chore/...`, open a PR, and
  merge only when every CI job is green. (The first scaffold commit was the one exception, D-009.)
- One resource, or one coherent slice, per PR: schema + client methods + tests + example + docs.
- Before asking for review: `make ci` passes locally, then run the ship-check skill.
- Commit messages: imperative subject, a body explaining why.

## Secrets and production
- Never commit `terraform.tfvars`, state, tokens, real tenant IDs, IPs, emails or customer names.
  Fixtures are scrubbed before they are committed.
- Never print a secret value, even when debugging. Check for presence or length only.
- Never run acceptance tests, `terraform apply`, or any write call (POST/PUT/DELETE) against MTN Cloud
  without explicit permission for that specific run. Read-only CLI calls are made by the owner.

## Changing upstream code
- Rename fully, delete dead branches (AWS, vSphere, other clouds), and add the tests upstream lacked.
- Moving a resource from `internal/legacy` to the Framework: same type name, same ID format, same
  attribute names. Add a state upgrader if needed. The plan must show no user-visible diff
  (proved by an acceptance step that applies with the legacy version, then plans with the new one).

## Records
- Decisions go in `dev-docs/decisions.md` (D-NNN). Questions, debt and build order go in
  `dev-docs/backlog.md`.
- `docs/` is generated, so never edit it by hand. Change `templates/` or `examples/`, then run `make docs`.
