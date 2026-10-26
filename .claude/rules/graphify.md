# graphify: the code graph

`graphify-out/graph.json` is a local AST graph of the Go code (tree-sitter, no LLM, nothing leaves the
machine). `.graphifyignore` limits it to code. Generated `docs/`, `dev-docs/`, `examples/`,
`templates/`, `.claude/`, Markdown and JSON are excluded on purpose. `graphify-out/` is gitignored.

## How it stays current
| Trigger | What runs |
|---|---|
| Claude edits a `.go` file | `.claude/hooks/graphify-update.mjs` runs `graphify update . --force` |
| `git commit` / checkout | `.git/hooks/post-commit`, `post-checkout` (installed by `graphify hook install`) |
| Manual | `graphify update . --force` |

`--force` is needed: without it, update refuses to write a graph with fewer nodes, so deleted code
would linger.

## Use it before grep
- `graphify explain "<Name>"`: the node and its edges. `<--` lines are dependents (blast radius).
- `graphify affected "<Name>" --depth 3`: transitive reverse impact.
- `graphify query "<question>" --budget 1500`: a scoped subgraph.
- `graphify god-nodes`: the hubs. Change these with extra care and extra tests.

## Trust
The graph is an index, never evidence. The source file decides what exists, tests decide behaviour,
and git decides what is current. "No callers" is the weakest claim: legacy resources are wired by
string keys in `internal/legacy/provider.go`'s `ResourcesMap`, and string-keyed wiring may not show as
a call edge. Always grep before deleting.

## Maintenance
After upgrading graphify, re-run `graphify hook install`, remove any `merge=graphify` line it adds to
`.gitattributes`, and run `git config --local --remove-section merge.graphify`.
