"""Verify complete coverage, bounded concurrency, and intact failure evidence."""
import importlib.util
import io
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from types import SimpleNamespace
from unittest.mock import patch

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("native_shards", Path(__file__).with_name("native-test-shards.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ShardContracts(unittest.TestCase):
    def test_windows_profile_keeps_every_parent_once_and_same_deadline(self):
        package = "github.com/ixayldz/Viber/internal/agent"
        names = [f"TestCase{index}" for index in range(70)] + ["FuzzInput", "ExampleRun"]
        for shards in (8, 16, 32):
            with self.subTest(shards=shards), patch.object(module, "checked", side_effect=[["other", package], names]), patch.object(module, "execute") as execute:
                module.main(agent_shards=shards, workers=4)
            commands = execute.call_args.args[0]
            self.assertEqual(len(commands), shards + 1)
            self.assertEqual(execute.call_args.kwargs, {"workers": 4})
            observed = []
            for command in commands[1:]:
                observed.extend(command[command.index("-run") + 1][4:-2].split("|"))
            self.assertEqual(sorted(observed), sorted(names))
            for command in commands:
                self.assertEqual(command[command.index("-timeout") + 1], "15m")

    def test_invalid_profiles_fail_before_discovery_or_execution(self):
        for shards, workers in [(0, 2), (33, 2), (8, 5), (2, 4)]:
            with self.subTest(shards=shards, workers=workers), patch.object(module, "checked") as checked, patch.object(module, "execute") as execute:
                with self.assertRaisesRegex(RuntimeError, "bounded native shard profile"):
                    module.main(agent_shards=shards, workers=workers)
                checked.assert_not_called()
                execute.assert_not_called()

    def test_race_keeps_owner_first_and_instruments_every_shard(self):
        package = "github.com/ixayldz/Viber/internal/agent"
        owner = "github.com/ixayldz/Viber/internal/owner"
        packages = ["cli", package, "resource", owner]
        names = ["TestA", "FuzzB", "ExampleC", "TestD", "TestE"]
        with patch.object(module, "checked", side_effect=[packages, names]) as checked, patch.object(module, "execute") as execute:
            module.main(race=True)
        self.assertIn("-race", checked.call_args_list[1].args[0])
        commands = execute.call_args.args[0]
        self.assertEqual(commands[0][-3:], [owner, "cli", "resource"])
        for command in commands:
            self.assertEqual(command.count("-race"), 1)
            self.assertEqual(command[command.index("-timeout") + 1], "15m")
        observed = []
        for command in commands[1:]:
            observed.extend(command[command.index("-run") + 1][4:-2].split("|"))
        self.assertEqual(sorted(observed), sorted(names))

    def test_race_rejects_missing_owner_inventory(self):
        with patch.object(module, "checked", side_effect=[["other", "github.com/ixayldz/Viber/internal/agent"], ["TestA"]]), patch.object(module, "execute") as execute:
            with self.assertRaisesRegex(RuntimeError, "owner race inventory"):
                module.main(race=True)
        execute.assert_not_called()

    def test_inventory_distributes_every_test_fuzz_and_example_once(self):
        package = "github.com/ixayldz/Viber/internal/agent"
        names = ["TestZulu", "TestAlpha", "FuzzInput", "ExampleRun", "TestBeta"]
        with patch.object(module, "checked", side_effect=[["other", package], names + ["ok package"]]), patch.object(module, "execute") as execute:
            module.main()
        commands = execute.call_args.args[0]
        self.assertEqual(commands[0][-1], "other")
        observed = []
        for command in commands[1:]:
            self.assertEqual(command[-1], package)
            observed.extend(command[command.index("-run") + 1][4:-2].split("|"))
        self.assertEqual(sorted(observed), sorted(names))
        for command in commands:
            self.assertEqual(command[command.index("-p") + 1], "1")
            self.assertEqual(command[command.index("-timeout") + 1], "15m")

    def test_two_workers_preserve_serial_first_shard_and_complete_failure_output(self):
        lock = threading.Lock()
        barrier = threading.Barrier(2, timeout=5)
        active = 0
        maximum = 0
        started = []
        completed = []
        stdout, stderr = io.BytesIO(), io.BytesIO()

        def runner(index, total, argv, directory):
            nonlocal active, maximum
            with lock:
                if index:
                    self.assertIn(0, completed)
                else:
                    self.assertEqual(active, 0)
                active += 1
                maximum = max(maximum, active)
                started.append(index)
            if index:
                barrier.wait()
            output, errors = directory / f"{index}.jsonl", directory / f"{index}.stderr"
            output.write_bytes(f'{{"shard":{index},"complete":true}}\n'.encode())
            errors.write_bytes(f"stderr:{index}\n".encode())
            with lock:
                active -= 1
                completed.append(index)
            return (1 if index == 2 else 0), output, errors

        with patch.object(module, "run_shard", side_effect=runner), patch.object(module.sys, "stdout", SimpleNamespace(buffer=stdout)), patch.object(module.sys, "stderr", SimpleNamespace(buffer=stderr)):
            with self.assertRaisesRegex(RuntimeError, "shards \\[3\\]"):
                module.execute([["unused"] for _ in range(5)])
        self.assertEqual(maximum, 2)
        self.assertEqual(sorted(started), list(range(5)))
        self.assertEqual(sorted(completed), list(range(5)))
        self.assertEqual(stdout.getvalue(), b"".join(f'{{"shard":{index},"complete":true}}\n'.encode() for index in range(5)))
        self.assertEqual(stderr.getvalue(), b"".join(f"stderr:{index}\n".encode() for index in range(5)))

    def test_actual_subprocess_output_and_exit_code_are_preserved(self):
        with tempfile.TemporaryDirectory() as temporary:
            result = module.run_shard(0, 1, [sys.executable, "-c", "import sys; print('evidence'); print('diagnostic', file=sys.stderr); sys.exit(7)"], Path(temporary))
            self.assertEqual(result[0], 7)
            self.assertEqual(result[1].read_bytes().strip(), b"evidence")
            self.assertEqual(result[2].read_bytes().strip(), b"diagnostic")


if __name__ == "__main__":
    unittest.main()
