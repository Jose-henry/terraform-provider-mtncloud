#!/usr/bin/env bash
# Fails if the upstream product or vendor name, or a misspelling of it, appears anywhere in tracked
# files, except:
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
  | xargs -0 grep -nIiE 'morpheus|morhpeus|morpehus|morpheous|\bhpe\b' 2>/dev/null \
  | grep -v 'github.com/gomorpheus/morpheus-go-sdk' \
  | grep -v 'mtncloud:allow-upstream-name' || true)"

if [ -n "$hits" ]; then
  echo "FAIL: upstream product name found (rename it, or mark an API wire value with mtncloud:allow-upstream-name and a reason):"
  echo "$hits" | head -50
  exit 1
fi
# The cloud software's name stays out of what customers read: docs, templates, examples, README,
# CHANGELOG, and Go string literals (descriptions and errors). API wire values in Go are allowed:
# endpoints and option names inside client calls, and lines marked mtncloud:allow-cloud-name.
cloud="$(git ls-files -z --cached --others --exclude-standard -- docs templates examples README.md CHANGELOG.md \
  | xargs -0 grep -nIi 'openstack' 2>/dev/null || true)"
cloud+="$(git ls-files -z --cached --others --exclude-standard -- '*.go' \
  | grep -zv '_test\.go$' \
  | xargs -0 grep -nIE '"[^"]*[Oo]pen[Ss]tack[^"]*"' 2>/dev/null \
  | grep -vE '"/api/|c\.options\(|ProvisionTypeCode ==|mtncloud:allow-cloud-name' || true)"
if [ -n "$cloud" ]; then
  echo "FAIL: the cloud software's name is shown to customers (say MTN Cloud, or mark an API value with mtncloud:allow-cloud-name):"
  echo "$cloud" | head -50
  exit 1
fi
echo "OK: no upstream product name outside the allowed places"
