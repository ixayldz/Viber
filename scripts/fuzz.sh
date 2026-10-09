#!/usr/bin/env bash
set -euo pipefail
output=${1:-evidence}
mkdir -p "$output"
# Discovery commands run directly so their failures cannot be hidden by process substitution.
go list ./... > "$output/fuzz-packages.txt"
targets=0
while IFS= read -r package; do
  go test "$package" -list '^Fuzz' > "$output/fuzz-targets.txt"
  while IFS= read -r target; do
    [[ "$target" =~ ^Fuzz[A-Za-z0-9_]+$ ]] || continue
    targets=$((targets + 1))
    name="${package##*/}-$target"
    go test "$package" -run '^$' -fuzz "^$target$" -fuzztime 15s -parallel 2 -timeout 2m 2>&1 | tee "$output/fuzz-$name.txt"
  done < "$output/fuzz-targets.txt"
done < "$output/fuzz-packages.txt"
test "$targets" -gt 0
printf 'Fuzz targets passed: %s\n' "$targets" | tee "$output/fuzz-summary.txt"
