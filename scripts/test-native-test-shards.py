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
