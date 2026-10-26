#!/usr/bin/env bash
# Fails if the upstream product name appears anywhere in tracked files, except:
#   - go.mod / go.sum and import lines of the external SDK (github.com/gomorpheus/morpheus-go-sdk)
#   - LICENSE (the MIT notice must be kept)
#   - lines marked "mtncloud:allow-upstream-name" (API wire values, with a reason)
#   - internal engineering notes (CLAUDE.md, dev-docs/, .claude/), which must name upstream tools
#     (the CLI binary, the Context7 library ID) to be useful; nothing there ships to users
#   - this script
set -euo pipefail
cd "$(dirname "$0")/.."

hits="$(git ls-files -z --cached --others --exclude-standard \
  | grep -zvE '^(go\.mod|go\.sum|LICENSE|CLAUDE\.md|scripts/check-names\.sh)$|^(dev-docs|\.claude)/' \
  | xargs -0 grep -nIi 'morpheus' 2>/dev/null \
  | grep -v 'github.com/gomorpheus/morpheus-go-sdk' \
  | grep -v 'mtncloud:allow-upstream-name' || true)"

if [ -n "$hits" ]; then
  echo "FAIL: upstream product name found (rename it, or mark an API wire value with mtncloud:allow-upstream-name and a reason):"
  echo "$hits" | head -50
  exit 1
fi
echo "OK: no upstream product name outside the allowed places"
