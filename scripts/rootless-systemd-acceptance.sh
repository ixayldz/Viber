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
task_apt_source="/etc/apt/sources.list.d/$task_user.sources"
task_apt_key="/etc/apt/keyrings/$task_user.asc"
task_apt_source_owned=false
task_apt_key_owned=false
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
      if sudo pgrep -u "$task_uid" >/dev/null; then task_cleanup=false; fi
      sudo userdel --remove -- "$task_user" || task_cleanup=false
      if getent passwd "$task_user" >/dev/null || [[ -e $task_home ]] || ! sudo test ! -S "/run/user/$task_uid/docker.sock"; then task_cleanup=false; fi
    fi
  fi
  if [[ $task_dropin_owned == true ]]; then
    sudo rm -f -- "$task_dropin" || task_cleanup=false
    sudo rmdir -- "$(dirname "$task_dropin")" || task_cleanup=false
    sudo systemctl daemon-reload || task_cleanup=false
  fi
  if [[ $task_apt_source_owned == true ]]; then sudo rm -- "$task_apt_source" || task_cleanup=false; fi
  if [[ $task_apt_key_owned == true ]]; then sudo rm -- "$task_apt_key" || task_cleanup=false; fi
  if [[ $task_apt_source_owned == true && -e $task_apt_source || $task_apt_key_owned == true && -e $task_apt_key ]]; then task_cleanup=false; fi
  printf '{"schema_version":1,"cleanup_verified":%s,"measurement_exit_code":%s}\n' "$task_cleanup" "$task_exit" > "$task_evidence/rootless-cleanup.json"
  if [[ $task_cleanup != true ]]; then echo 'Exact rootless fixture cleanup failed.' >&2; exit 1; fi
  exit "$task_exit"
}
trap cleanup EXIT

# runner-images removes its Docker apt source after building the image. Restore
# a job-owned signed source, without replacing any existing repository/key or
# changing the installed engine. Rootless extras must match that exact engine.
# See https://docs.docker.com/engine/install/ubuntu/#install-using-the-apt-repository
# Official packages include Ubuntu's rootlesskit AppArmor profile; never disable
# AppArmor/seccomp or unprivileged namespace restrictions.
task_engine_version=$(dpkg-query -W -f='${Version}' docker-ce)
[[ -n $task_engine_version && ! -e $task_apt_source && ! -e $task_apt_key ]]
task_codename=$(. /etc/os-release; printf '%s' "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
task_arch=$(dpkg --print-architecture)
[[ $task_codename =~ ^[a-z]+$ && $task_arch =~ ^[a-z0-9]+$ ]]
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://download.docker.com/linux/ubuntu/gpg -o "$task_evidence/docker-repository.asc"
sudo install -d -m 0755 /etc/apt/keyrings
sudo install -m 0644 "$task_evidence/docker-repository.asc" "$task_apt_key"
task_apt_key_owned=true
printf 'Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: %s\n' \
  "$task_codename" "$task_arch" "$task_apt_key" | sudo tee "$task_apt_source" >/dev/null
task_apt_source_owned=true
sudo apt-get update
sudo apt-get install --yes --no-install-recommends "docker-ce-rootless-extras=$task_engine_version" uidmap dbus-user-session slirp4netns
[[ $(dpkg-query -W -f='${Version}' docker-ce) == "$task_engine_version" ]]
dpkg-query -W docker-ce docker-ce-rootless-extras uidmap dbus-user-session slirp4netns > "$task_evidence/rootless-packages.txt"
sudo useradd --create-home --user-group --shell /bin/bash "$task_user"
task_created=true
task_uid=$(id -u "$task_user")
[[ $task_uid =~ ^[0-9]+$ && $task_uid != 0 && $task_uid != "$(id -u)" && $(getent passwd "$task_user" | cut -d: -f6) == "$task_home" ]]
task_dropin="/etc/systemd/system/user@$task_uid.service.d/viber-acceptance.conf"
[[ ! -e $(dirname "$task_dropin") ]]
sudo mkdir -- "$(dirname "$task_dropin")"
task_dropin_owned=true
[[ -x /usr/lib/systemd/systemd ]]
# runner-images puts runner-specific XDG paths in /etc/environment. PAM applies
# those after unit Environment=, so override only this fresh UID's ExecStart
# environment after PAM. Keep the original systemd --user manager and sandbox;
# do not edit the host's global PAM/environment configuration.
printf '[Service]\nDelegate=cpu cpuset io memory pids\nMemoryMax=3G\nCPUQuota=200%%\nTasksMax=512\nExecStart=\nExecStart=/usr/bin/env XDG_RUNTIME_DIR=/run/user/%%i XDG_CONFIG_HOME=%s/.config XDG_DATA_HOME=%s/.local/share XDG_CACHE_HOME=%s/.cache XDG_STATE_HOME=%s/.local/state DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%%i/bus /usr/lib/systemd/systemd --user\n' \
  "$task_home" "$task_home" "$task_home" "$task_home" | sudo tee "$task_dropin" >/dev/null
sudo systemctl daemon-reload
sudo loginctl enable-linger "$task_user"
sudo systemctl start "user@$task_uid.service"
task_manager_pid=$(sudo systemctl show "user@$task_uid.service" --property MainPID --value)
[[ $task_manager_pid =~ ^[0-9]+$ && $task_manager_pid != 0 ]]
sudo python3 - "$task_uid" "$task_home" "$task_manager_pid" > "$task_evidence/rootless-manager-profile.json" <<'PY'
import json, os, stat, sys
uid, home, pid = int(sys.argv[1]), sys.argv[2], int(sys.argv[3])
runtime = '/run/user/' + str(uid)
assert os.stat('/proc/' + str(pid)).st_uid == uid
environment = dict(item.split(b'=', 1) for item in open('/proc/' + str(pid) + '/environ', 'rb').read().split(b'\0') if b'=' in item)
expected = {'XDG_RUNTIME_DIR': runtime, 'XDG_CONFIG_HOME': home + '/.config', 'XDG_DATA_HOME': home + '/.local/share', 'XDG_CACHE_HOME': home + '/.cache', 'XDG_STATE_HOME': home + '/.local/state', 'DBUS_SESSION_BUS_ADDRESS': 'unix:path=' + runtime + '/bus'}
for key, value in expected.items():
    assert environment.get(key.encode()) == value.encode(), key
info = os.stat(runtime)
assert info.st_uid == uid and stat.S_IMODE(info.st_mode) == 0o700
print(json.dumps({'schema_version': 1, 'uid': uid, 'manager_pid': pid, 'runtime_mode': '0700', 'paths': expected}))
PY
task_runtime="/run/user/$task_uid"
task_endpoint="unix://$task_runtime/docker.sock"
task_as_user=(sudo -H -u "$task_user" env -u DOCKER_HOST -u DOCKER_CONTEXT "XDG_RUNTIME_DIR=$task_runtime" "DBUS_SESSION_BUS_ADDRESS=unix:path=$task_runtime/bus" "XDG_CONFIG_HOME=$task_home/.config" "XDG_DATA_HOME=$task_home/.local/share" "XDG_CACHE_HOME=$task_home/.cache" "XDG_STATE_HOME=$task_home/.local/state" PATH=/usr/bin:/bin)
# /run/user/<uid> is deliberately mode0700. The CI runner account cannot stat
# another user's bus; probe as its owner instead of misreporting safe isolation
# as a missing socket. Bind XDG paths in the manager and caller independently.
for task_attempt in {1..30}; do "${task_as_user[@]}" test -S "$task_runtime/bus" && break; sleep 1; done
"${task_as_user[@]}" test -S "$task_runtime/bus"
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
