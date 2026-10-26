# Tools

## Documentation: Context7 first
- Platform API: `/websites/apidocs_morpheusdata_reference` (the OpenAPI reference). One concept per
  query, for example "create security group rule body", not "networking".
- Terraform Plugin Framework, plugin-testing, mux, tfplugindocs: resolve the library ID with Context7
  and query it. Don't rely on memory for framework APIs.
- OpenStack behaviour (Neutron routers, floating IPs, Octavia): Context7 first, then the OpenStack docs.

## Real payloads: the platform CLI (owner's machine)
- **CLI docs:** `../HPE_sd00006978en_us_HPE Morpheus Enterprise Software CLI Documentation Reference (1).pdf`
  (one folder above the repo). When unsure about a command, flag or payload, look it up there
  before guessing.
- Claude may run the CLI in the owner's PowerShell, **always through `scripts/cli/capture.ps1`**,
  which adds `-j -V` (JSON response plus the request, bearer token masked).
- **Reads only** (`list`, `get`, `types`, `whoami`). Anything that could write (`add`, `update`,
  `remove`, `allocate`, `refresh`, ...) runs **only as a dry run (`-d`)**, which prints the request
  without sending it. The script refuses otherwise. No exceptions without the owner's explicit
  approval for that one command.
- Output lands in `.captures/` (git-ignored): it holds real tenant data. Summarise it with
  `scripts/cli/summarize.py` (redacts secrets), record only scrubbed facts in `dev-docs/`, and
  scrub fixtures before committing them.
- Check which login the CLI uses (`whoami`): master and sub-tenant tokens see different data.

## Code graph
Use `graphify` (see graphify.md) before grep for "who calls this" and "what breaks".

## Local toolchain
- Go (version in `go.mod`), golangci-lint v2, the Terraform CLI, Python 3 (used by
  `scripts/validate-examples.sh`).
- On Windows, `-race` needs cgo, so race tests run in CI (Linux) or WSL.
- tfplugindocs runs through `go run` (pinned in `go.mod` via `tools/tools.go`).
