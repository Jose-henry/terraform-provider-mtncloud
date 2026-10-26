# Tools

## Documentation: Context7 first
- Platform API: `/websites/apidocs_morpheusdata_reference` (the OpenAPI reference). One concept per
  query, for example "create security group rule body", not "networking".
- Terraform Plugin Framework, plugin-testing, mux, tfplugindocs: resolve the library ID with Context7
  and query it. Don't rely on memory for framework APIs.
- OpenStack behaviour (Neutron routers, floating IPs, Octavia): Context7 first, then the OpenStack docs.

## Real payloads: the owner runs the CLI
The upstream CLI is installed on the owner's machine (`<cli> <noun> list|get ... -j`, plus `-V` to
print the request). Claude never runs it with the owner's credentials. Ask for one specific read-only
command and say why it's needed. The payloads still needed are listed in `dev-docs/api-research.md`.
Save results as scrubbed fixtures.

## Code graph
Use `graphify` (see graphify.md) before grep for "who calls this" and "what breaks".

## Local toolchain
- Go (version in `go.mod`), golangci-lint v2, the Terraform CLI, Python 3 (used by
  `scripts/validate-examples.sh`).
- On Windows, `-race` needs cgo, so race tests run in CI (Linux) or WSL.
- tfplugindocs runs through `go run` (pinned in `go.mod` via `tools/tools.go`).
