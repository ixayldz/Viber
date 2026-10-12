"""Run every native Go test, bounding the large agent package in four shards.

No test is filtered out: discover Test/Fuzz/Example names and distribute all of
them. Execution stays serial to avoid competing physical-disk fixtures. JSONL
from all invocations remains one evidence stream for the existing validator.
"""
import re
import subprocess
import sys


def checked(argv):
    result = subprocess.run(argv, check=True, capture_output=True, text=True)
    return result.stdout.splitlines()


def main():
    package = "github.com/ixayldz/Viber/internal/agent"
    packages = checked(["go", "list", "./..."])
    if packages.count(package) != 1 or len(packages) < 2:
        raise RuntimeError("complete native package inventory unavailable")
    names = [line for line in checked(["go", "test", "-list", ".", package])
             if re.fullmatch(r"(?:Test|Fuzz|Example)[A-Za-z0-9_]+", line)]
    if not names or len(names) != len(set(names)):
        raise RuntimeError("complete agent test inventory unavailable")
    names.sort()
    shards = [names[index::4] for index in range(4)]
    if sorted(name for shard in shards for name in shard) != names:
        raise RuntimeError("native shard coverage mismatch")
    commands = [["go", "test", "-p", "1", "-json", "-count=1", "-timeout", "15m"]
                + [name for name in packages if name != package]]
    commands += [["go", "test", "-p", "1", "-json", "-count=1", "-timeout", "15m",
                  "-run", "^(?:" + "|".join(shard) + ")$", package]
                 for shard in shards if shard]
    failures = []
    for index, argv in enumerate(commands):
        print(f"native complete-suite shard {index + 1}/{len(commands)}", file=sys.stderr)
        result = subprocess.run(argv)
        if result.returncode:
            failures.append(index + 1)
    if failures:
        raise RuntimeError(f"native acceptance failed in shards {failures}")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
