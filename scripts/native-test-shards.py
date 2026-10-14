"""Run every native Go test, bounding the large agent package in complete shards.

No test is filtered out: discover Test/Fuzz/Example names and distribute all of
them. Other packages run first and serially, preserving owner/CLI isolation.
Agent shards default to two workers; Windows CI explicitly uses sixteen shards
and four workers to keep whole-shard duration within the unchanged timeout.
Each worker has a separate
output file; complete JSONL is merged in shard order without interleaving.
With --race every discovered package and shard uses the race detector, and the
owner package runs first in the serial package batch.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


def checked(argv):
    result = subprocess.run(argv, check=True, capture_output=True, text=True)
    return result.stdout.splitlines()


def run_shard(index, total, argv, directory):
    print(f"native complete-suite shard {index + 1}/{total}", file=sys.stderr, flush=True)
    output = directory / f"shard-{index}.jsonl"
    errors = directory / f"shard-{index}.stderr"
    with output.open("wb") as stdout, errors.open("wb") as stderr:
        result = subprocess.run(argv, stdout=stdout, stderr=stderr)
    return result.returncode, output, errors


def emit_result(result):
    returncode, output, errors = result
    with output.open("rb") as source:
        shutil.copyfileobj(source, sys.stdout.buffer)
    sys.stdout.buffer.flush()
    with errors.open("rb") as source:
        shutil.copyfileobj(source, sys.stderr.buffer)
    sys.stderr.buffer.flush()
    return returncode


def execute(commands, workers=2):
    if not 1 <= workers <= 4:
        raise RuntimeError("native worker bound exceeded")
    failures = []
    with tempfile.TemporaryDirectory(prefix="viber-native-shards-") as temporary:
        directory = Path(temporary)
        # Package parallelism remains one; CLI/owner fixtures finish before
        # any agent shard starts. Never emit concurrent writers into JSONL.
        if emit_result(run_shard(0, len(commands), commands[0], directory)):
            failures.append(1)
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = [pool.submit(run_shard, index, len(commands), argv, directory)
                       for index, argv in enumerate(commands[1:], 1)]
            for index, future in enumerate(futures, 2):
                if emit_result(future.result()):
                    failures.append(index)
    if failures:
        raise RuntimeError(f"native acceptance failed in shards {failures}")


def main(race=False, agent_shards=4, workers=2):
    if not 1 <= agent_shards <= 16 or not 1 <= workers <= min(4, agent_shards):
        raise RuntimeError("invalid bounded native shard profile")
    package = "github.com/ixayldz/Viber/internal/agent"
    packages = checked(["go", "list", "./..."])
    if packages.count(package) != 1 or len(packages) < 2:
        raise RuntimeError("complete native package inventory unavailable")
    race_flags = ["-race"] if race else []
    names = [line for line in checked(["go", "test"] + race_flags + ["-list", ".", package])
             if re.fullmatch(r"(?:Test|Fuzz|Example)[A-Za-z0-9_]+", line)]
    if not names or len(names) != len(set(names)):
        raise RuntimeError("complete agent test inventory unavailable")
    names.sort()
    shards = [names[index::agent_shards] for index in range(agent_shards)]
    if sorted(name for shard in shards for name in shard) != names:
        raise RuntimeError("native shard coverage mismatch")
    other_packages = [name for name in packages if name != package]
    if race:
        owner = "github.com/ixayldz/Viber/internal/owner"
        if other_packages.count(owner) != 1:
            raise RuntimeError("owner race inventory unavailable")
        other_packages = [owner] + [name for name in other_packages if name != owner]
    commands = [["go", "test"] + race_flags + ["-p", "1", "-json", "-count=1", "-timeout", "15m"]
                + other_packages]
    commands += [["go", "test"] + race_flags + ["-p", "1", "-json", "-count=1", "-timeout", "15m",
                  "-run", "^(?:" + "|".join(shard) + ")$", package]
                 for shard in shards if shard]
    execute(commands, workers=workers)


if __name__ == "__main__":
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--race", action="store_true", help="run every package with the race detector")
        parser.add_argument("--agent-shards", type=int, default=4, help="complete agent shards, 1..16")
        parser.add_argument("--workers", type=int, default=2, help="bounded concurrent agent processes, 1..4")
        options = parser.parse_args()
        main(race=options.race, agent_shards=options.agent_shards, workers=options.workers)
    except (RuntimeError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
