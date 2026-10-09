#!/usr/bin/env bash
set -euo pipefail
evidence=${1:?evidence directory required}
mkdir -p "$evidence"
if [[ "$(uname -s)" == Linux ]]; then
  # dbus-run-session supplies a fresh local session bus. The default login
  # collection belongs only to the disposable CI runner user.
  printf '%s' 'viber-ephemeral-ci-unlock' | gnome-keyring-daemon --unlock --components=secrets >/dev/null
fi
export VIBER_OS_VAULT_TEST=1
go test -json -count=1 -timeout 2m ./internal/auth -run '^TestActualOSVaultRoundTripAndEnvironmentIsolation$' | tee "$evidence/vault.jsonl"
python3 scripts/verify-test-results.py "$evidence/vault.jsonl" --vault
