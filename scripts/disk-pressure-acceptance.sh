#!/usr/bin/env bash
# Trusted test binary only. No privileged mode, host socket or writable mounts.
set -euo pipefail
task_evidence="${1:-evidence}"
mkdir -p "$task_evidence"
task_binary="$task_evidence/diskguard-pressure.test"
go test -c -o "$task_binary" ./internal/diskguard
task_binary="$(realpath "$task_binary")"
task_image='public.ecr.aws/docker/library/golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61'
docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges \
  --read-only --cpus 2 --memory 512m --pids-limit 64 \
  --tmpfs /pressure:rw,noexec,nosuid,nodev,size=128m \
  --mount "type=bind,source=$task_binary,target=/diskguard-pressure.test,readonly" \
  --env VIBER_BOUNDED_DISK_PRESSURE=/pressure \
  "$task_image" /diskguard-pressure.test -test.v=test2json -test.count=1 \
  -test.timeout=2m \
  '-test.run=^TestActualBoundedFilesystemPressurePreservesControlPublicationAndReopen$' \
  | go tool test2json -p github.com/ixayldz/Viber/internal/diskguard -t \
  | tee "$task_evidence/disk-pressure.jsonl"
python3 scripts/verify-test-results.py "$task_evidence/disk-pressure.jsonl" --disk-pressure
