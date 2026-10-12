"""Run every selected migration parent three times with its own bounded process.

Go's -timeout applies to a package process, including all -count repetitions.
Durable Windows fixtures can exhaust a shared five-minute package deadline even
while every completed iteration passes. Keep the selection, three repetitions,
JSONL evidence and five-minute bound, but isolate each parent. Run serially so
SQLite/control-reserve fixtures do not compete for physical disk flushes.
"""
import re
import subprocess
import sys

TARGETS = ("./internal/store", "./internal/agent", "./internal/cli")
SELECTION = "Migration|LegacyOpen|FormatAndMigration"
TEST_NAME = re.compile(r"(?:Test|Fuzz|Example)[A-Za-z0-9_]+")


def checked(argv):
    result = subprocess.run(argv, check=True, capture_output=True, text=True,
                            timeout=120)
    if len(result.stdout) > 128 << 10:
        raise RuntimeError("migration inventory exceeds bounded output")
    return result.stdout.splitlines()


def discover():
    packages = checked(["go", "list", *TARGETS])
    if len(packages) != len(TARGETS) or len(set(packages)) != len(packages):
        raise RuntimeError("complete migration package inventory unavailable")
    parents = []
    for package in packages:
        names = [name for name in checked(["go", "test", "-list", SELECTION, package])
                 if TEST_NAME.fullmatch(name)]
        if not names or len(names) > 256 or len(set(names)) != len(names):
            raise RuntimeError("complete migration parent inventory unavailable")
        if any(not re.search(SELECTION, name) for name in names):
            raise RuntimeError("migration inventory selection mismatch")
        parents.extend((package, name) for name in sorted(names))
    return parents


def command(package, name):
    return ["go", "test", "-p", "1", "-json", "-count=3", "-timeout", "5m",
            "-run", "^" + re.escape(name) + "$", package]


def main():
    parents = discover()
    for index, (package, name) in enumerate(parents, 1):
        print(f"migration parent {index}/{len(parents)}: {package}:{name}; repetitions=3",
              file=sys.stderr, flush=True)
        subprocess.run(command(package, name), check=True)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
