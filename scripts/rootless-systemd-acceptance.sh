#!/usr/bin/env bash
# Only a disposable GitHub-hosted Linux runner may be configured. Never run
# this installer against a developer workstation or a self-hosted runner.
set -euo pipefail
if [[ ${1:-} != --allow-configure-disposable-github-hosted-runner || ${GITHUB_ACTIONS:-} != true || ${RUNNER_ENVIRONMENT:-} != github-hosted || $(uname -s) != Linux || $(id -u) == 0 ]]; then
  echo 'Disposable GitHub-hosted Linux runner opt-in required; no changes made.' >&2
  exit 4
fi
if [[ ! ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ || ! ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]; then exit 4; fi
task_evidence=$(realpath -m "${2:-evidence}")
mkdir -p "$task_evidence"
task_user="vbr-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
[[ ${#task_user} -le 32 && ! $(getent passwd "$task_user" || true) ]]
task_home="/home/$task_user"
[[ ! -e $task_home ]]
task_uid=''
task_created=false
task_dropin=''
task_dropin_owned=false
task_image='public.ecr.aws/docker/library/golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61'
task_binary="$task_evidence/rootless-runner.test"
CGO_ENABLED=0 go test -c -o "$task_binary" ./internal/runner

cleanup() {
  task_exit=$?
  trap - EXIT
  set +e
  task_cleanup=true
  if [[ $task_created == true ]]; then
    # Revalidate exact created UID/home before terminating or removing anything.
    task_account=$(getent passwd "$task_user")
    if [[ $(cut -d: -f3 <<<"$task_account") != "$task_uid" || $(cut -d: -f6 <<<"$task_account") != "$task_home" ]]; then
      task_cleanup=false
    else
      sudo -H -u "$task_user" env XDG_RUNTIME_DIR="/run/user/$task_uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$task_uid/bus" systemctl --user stop docker.service
      sudo journalctl --no-pager -u "user@$task_uid.service" > "$task_evidence/rootless-service.log"
      sudo loginctl disable-linger "$task_user" || task_cleanup=false
      sudo loginctl terminate-user "$task_user" || true
      sudo systemctl stop "user@$task_uid.service" || task_cleanup=false
      if pgrep -u "$task_uid" >/dev/null; then task_cleanup=false; fi
      sudo userdel --remove -- "$task_user" || task_cleanup=false
      if getent passwd "$task_user" >/dev/null || [[ -e $task_home || -S /run/user/$task_uid/docker.sock ]]; then task_cleanup=false; fi
    fi
  fi
  if [[ $task_dropin_owned == true ]]; then
    sudo rm -f -- "$task_dropin" || task_cleanup=false
    sudo rmdir -- "$(dirname "$task_dropin")" || task_cleanup=false
    sudo systemctl daemon-reload || task_cleanup=false
  fi
  printf '{"schema_version":1,"cleanup_verified":%s,"measurement_exit_code":%s}\n' "$task_cleanup" "$task_exit" > "$task_evidence/rootless-cleanup.json"
  if [[ $task_cleanup != true ]]; then echo 'Exact rootless fixture cleanup failed.' >&2; exit 1; fi
  exit "$task_exit"
}
trap cleanup EXIT

# Official distribution packages include Ubuntu's rootlesskit AppArmor profile;
# do not disable AppArmor/seccomp or unprivileged namespace restrictions.
sudo apt-get update
sudo apt-get install --yes docker-ce-rootless-extras uidmap dbus-user-session slirp4netns
sudo useradd --create-home --user-group --shell /bin/bash "$task_user"
task_created=true
task_uid=$(id -u "$task_user")
[[ $task_uid =~ ^[0-9]+$ && $task_uid != 0 && $task_uid != "$(id -u)" && $(getent passwd "$task_user" | cut -d: -f6) == "$task_home" ]]
task_dropin="/etc/systemd/system/user@$task_uid.service.d/viber-acceptance.conf"
[[ ! -e $(dirname "$task_dropin") ]]
sudo mkdir -- "$(dirname "$task_dropin")"
task_dropin_owned=true
printf '[Service]\nDelegate=cpu cpuset io memory pids\nMemoryMax=3G\nCPUQuota=200%%\nTasksMax=512\n' | sudo tee "$task_dropin" >/dev/null
sudo systemctl daemon-reload
sudo loginctl enable-linger "$task_user"
sudo systemctl start "user@$task_uid.service"
task_runtime="/run/user/$task_uid"
task_endpoint="unix://$task_runtime/docker.sock"
task_as_user=(sudo -H -u "$task_user" env "XDG_RUNTIME_DIR=$task_runtime" "DBUS_SESSION_BUS_ADDRESS=unix:path=$task_runtime/bus" PATH=/usr/bin:/bin)
for task_attempt in {1..30}; do [[ -S $task_runtime/bus ]] && break; sleep 1; done
[[ -S $task_runtime/bus ]]
"${task_as_user[@]}" dockerd-rootless-setuptool.sh install --force
sudo install -d -o "$task_user" -g "$task_user" -m 0755 "$task_home/matrix"
sudo install -o "$task_user" -g "$task_user" -m 0755 "$task_binary" "$task_home/matrix/runner.test"
"${task_as_user[@]}" docker --host "$task_endpoint" info --format '{{json .}}' > "$task_evidence/rootless-before-info.json"
python3 - "$task_evidence/rootless-before-info.json" <<'PY'
import json, sys
info = json.load(open(sys.argv[1]))
assert 'name=rootless' in info['SecurityOptions']
assert info['CgroupVersion'] == '2' and info['CgroupDriver'] == 'systemd'
assert all(info.get(key) is True for key in ('MemoryLimit', 'SwapLimit', 'CpuCfsQuota', 'CpuCfsPeriod', 'PidsLimit'))
assert info['ID']
PY
"${task_as_user[@]}" docker --host "$task_endpoint" pull "$task_image"
run_test() {
  task_phase=$1
  task_pattern=$2
  task_output=$3
  "${task_as_user[@]}" env "VIBER_DOCKER_HOST=$task_endpoint" "VIBER_DOCKER_TEST_IMAGE=$task_image" "VIBER_ENGINE_MATRIX_DIR=$task_home/matrix" "VIBER_ENGINE_MATRIX_PHASE=$task_phase" \
    "$task_home/matrix/runner.test" -test.v=test2json -test.count=1 -test.timeout=10m "-test.run=$task_pattern" \
    | go tool test2json -t -p github.com/ixayldz/Viber/internal/runner | tee "$task_evidence/$task_output"
  python3 scripts/verify-test-results.py "$task_evidence/$task_output"
}
run_test rootless-probe '^TestDisposableRootlessCapability$' rootless-probe.jsonl
grep -q ROOTLESS_SUPPORTED "$task_evidence/rootless-probe.jsonl"
if grep -q ROOTLESS_DENIED "$task_evidence/rootless-probe.jsonl"; then exit 1; fi
run_test before '^TestDisposableEnginePhase$' rootless-before.jsonl
grep -q BEFORE_RESTART_OWNED_SUBJECT_RUNNING "$task_evidence/rootless-before.jsonl"
"${task_as_user[@]}" systemctl --user restart docker.service
"${task_as_user[@]}" docker --host "$task_endpoint" info --format '{{json .}}' > "$task_evidence/rootless-after-info.json"
python3 - "$task_evidence/rootless-before-info.json" "$task_evidence/rootless-after-info.json" <<'PY'
import json, sys
before, after = [json.load(open(path)) for path in sys.argv[1:]]
assert before['ID'] == after['ID'] and 'name=rootless' in after['SecurityOptions']
assert after['CgroupVersion'] == '2' and after['CgroupDriver'] == 'systemd'
assert all(after.get(key) is True for key in ('MemoryLimit', 'SwapLimit', 'CpuCfsQuota', 'CpuCfsPeriod', 'PidsLimit'))
PY
run_test after '^TestDisposableEnginePhase$' rootless-after.jsonl
grep -q AFTER_RESTART_FENCED "$task_evidence/rootless-after.jsonl"
run_test native '^(TestActualDockerHostileProcessMatrix|TestActualDockerPersistentFenceInventoryAndReopen|TestDockerOfflineReadOnlyQuiescenceAndTimeout|TestActualDockerNativeOrphanFencingPreventsLateCreateAndStart)$' rootless-native.jsonl
python3 scripts/verify-test-results.py "$task_evidence/rootless-native.jsonl" --rootless
