"""Runner.run happy path + failure mapping with an injected fake Popen."""

from __future__ import annotations

import unittest

from _common import FakePopenFactory, fake_which  # noqa: E402
from wt_ctl.errors import ExternalCommandFailed, ToolMissing  # noqa: E402
from wt_ctl.runner import Runner  # noqa: E402


class RunnerHappyTests(unittest.TestCase):
    def test_run_captures_stdout(self) -> None:
        factory = FakePopenFactory({("echo", "hi"): ("hi\n", "", 0)})
        r = Runner(popen_factory=factory, which=fake_which)
        result = r.run(["echo", "hi"])
        self.assertEqual(result.rc, 0)
        self.assertEqual(result.stdout, "hi\n")
        self.assertEqual(factory.calls, [["echo", "hi"]])

    def test_run_check_false_returns_nonzero(self) -> None:
        factory = FakePopenFactory({("git", "status"): ("", "boom", 7)})
        r = Runner(popen_factory=factory, which=fake_which)
        result = r.run(["git", "status"], check=False)
        self.assertEqual(result.rc, 7)
        self.assertIn("boom", result.stderr)


class RunnerFailureMappingTests(unittest.TestCase):
    def test_check_true_raises(self) -> None:
        factory = FakePopenFactory({("git", "fail"): ("", "msg", 2)})
        r = Runner(popen_factory=factory, which=fake_which)
        with self.assertRaises(ExternalCommandFailed) as ctx:
            r.run(["git", "fail"])
        self.assertEqual(ctx.exception.rc, 2)
        self.assertEqual(ctx.exception.argv, ["git", "fail"])

    def test_missing_tool_maps_to_ToolMissing(self) -> None:
        def boom(argv, **kw):
            raise FileNotFoundError("no such file: " + argv[0])

        r = Runner(popen_factory=boom, which=lambda _n: None)
        with self.assertRaises(ToolMissing):
            r.run(["nonexistent-tool"])


if __name__ == "__main__":
    unittest.main()
