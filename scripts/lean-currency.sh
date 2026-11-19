#!/usr/bin/env bash
# Checks that every `Mirrors <pkg>.<Name> (<file>)` citation in lean4/ still names an
# existing Go file that declares the symbol, then lists Lean files whose cited Go file changed since
# the commit in lean4/reviewed-commit (informational, never fails).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

cites="$(grep -rnE --include='*.lean' --exclude-dir=.lake 'Mirrors [A-Za-z0-9_]+\.[A-Za-z0-9_]+ \([^)]+\)' lean4 || true)"
if [ -z "$cites" ]; then
  echo "FAIL: no Mirrors citations found in lean4/" >&2
  exit 1
fi

# has_symbol is true when the Go file declares NAME as a function, method, type, var or const.
has_symbol() {
  grep -qE "^func (\([^)]*\) )?$2[(\[]|^(type|var|const) $2([[:space:]]|\$)|^[[:space:]]+$2[[:space:]]+(=|[A-Za-z*\[])" "$1"
}

fail=0
count=0
changed=""
reviewed=""
if [ -f lean4/reviewed-commit ]; then
  reviewed="$(tr -d '[:space:]' <lean4/reviewed-commit)"
fi

while IFS= read -r line; do
  lean_file="${line%%:*}"
  cite="$(printf '%s\n' "$line" | sed -E 's/.*Mirrors ([A-Za-z0-9_]+\.[A-Za-z0-9_]+) \(([^)]+)\).*/\1 \2/')"
  sym="${cite%% *}"
  gofile="${cite#* }"
  name="${sym#*.}"
  count=$((count + 1))
  if [ ! -f "$gofile" ]; then
    echo "FAIL: $lean_file cites $sym but $gofile does not exist" >&2
    fail=1
    continue
  fi
  if ! has_symbol "$gofile" "$name"; then
    echo "FAIL: $lean_file cites $sym but $gofile declares no $name" >&2
    fail=1
    continue
  fi
  if [ -n "$reviewed" ] && ! git diff --quiet "$reviewed" -- "$gofile"; then
    changed="$changed$lean_file (cites $gofile)"$'\n'
  fi
done <<<"$cites"

if [ -n "$changed" ]; then
  echo "Lean files whose cited Go file changed since reviewed-commit ${reviewed:0:12}:"
  printf '%s' "$changed" | sort -u
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "PASS: $count Mirrors citations resolve"
