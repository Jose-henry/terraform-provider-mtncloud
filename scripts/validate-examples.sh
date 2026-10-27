#!/usr/bin/env bash
# Validates every example .tf file against the provider's real schema.
#
# Each file is validated on its own (example folders hold alternative variants that reuse
# resource names). Only schema errors count: unknown/missing arguments, wrong types, bad
# blocks. References to resources defined in other examples and file() paths are expected in
# documentation snippets and are ignored.
#
# Known upstream defects are listed in scripts/examples-known-defects.txt. The script fails on
# any defect NOT in that list, and on any listed file that is now clean (so the list only
# shrinks). Needs: go, terraform, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PY="${PYTHON:-python3}"
TF="${TERRAFORM:-terraform}"
KNOWN="$ROOT/scripts/examples-known-defects.txt"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> building provider"
mkdir -p "$WORK/plugins"
(cd "$ROOT" && go build -o "$WORK/plugins/terraform-provider-mtncloud$(go env GOEXE)" .)
# Native Windows terraform can't resolve Git Bash paths like /tmp/...
PLUGIN_DIR="$WORK/plugins"
command -v cygpath > /dev/null && PLUGIN_DIR="$(cygpath -m "$PLUGIN_DIR")"
cat > "$WORK/dev.tfrc" <<EOF
provider_installation {
  dev_overrides {
    "jose-henry/mtncloud" = "$PLUGIN_DIR"
  }
  direct {}
}
EOF
export TF_CLI_CONFIG_FILE="$WORK/dev.tfrc"

SCHEMA_ERRORS='["Unsupported argument","Missing required argument","Incorrect attribute value type","Unsupported block type","Missing required block","Too many blocks","Invalid or unknown key"]'

found="$WORK/found.txt"
: > "$found"
total=0
while IFS= read -r -d '' f; do
  total=$((total + 1))
  rel="${f#"$ROOT"/}"
  w="$WORK/case"
  rm -rf "$w" && mkdir -p "$w"
  cp "$f" "$w/main.tf"
  printf 'terraform {\n  required_providers {\n    mtncloud = { source = "jose-henry/mtncloud" }\n  }\n}\n' > "$w/zz_required.tf"
  out="$(cd "$w" && "$TF" validate -no-color -json 2>/dev/null || true)"
  # Exit 0 = clean, 1 = schema defect, 2 = the check itself could not run (provider not
  # loaded, bad output). 2 aborts: a broken harness must never look like a clean example.
  rc=0
  echo "$out" | "$PY" -c "
import json, sys
kinds = set(json.loads('$SCHEMA_ERRORS'))
try:
    d = json.load(sys.stdin)
except ValueError:
    sys.exit(2)
errs = [x for x in d.get('diagnostics', []) if x['severity'] == 'error']
if any('plugin' in x['summary'].lower() or 'provider' in x['summary'].lower() and 'schema' in x['summary'].lower() for x in errs):
    sys.exit(2)
sys.exit(1 if any(x['summary'] in kinds for x in errs) else 0)
" || rc=$?
  case "$rc" in
    0) ;;
    1) echo "$rel" >> "$found" ;;
    *) echo "ERROR: could not validate $rel (provider failed to load?):"; echo "$out" | head -20; exit 2 ;;
  esac
done < <(find "$ROOT/examples/resources" "$ROOT/examples/data-sources" "$ROOT/examples/quickstart" -name '*.tf' -print0 | sort -z)

known_sorted="$WORK/known.txt"
grep -vE '^\s*(#|$)' "$KNOWN" | sort > "$known_sorted"
sort -o "$found" "$found"

new="$(comm -13 "$known_sorted" "$found")"
fixed="$(comm -23 "$known_sorted" "$found")"

echo "==> $total example files checked, $(wc -l < "$found" | tr -d ' ') with known schema defects"
status=0
if [ -n "$new" ]; then
  echo "FAIL: new schema defects in examples (fix them; never add to the known list for new code):"
  echo "$new" | sed 's/^/  /'
  status=1
fi
if [ -n "$fixed" ]; then
  echo "FAIL: these files are now clean, remove them from scripts/examples-known-defects.txt:"
  echo "$fixed" | sed 's/^/  /'
  status=1
fi
[ "$status" -eq 0 ] && echo "OK"
exit "$status"
