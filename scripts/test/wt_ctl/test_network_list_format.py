"""Regression test against the captured pre-Phase-2.5 bash output.

The fixtures at ``scripts/test/wt_ctl/fixtures/network_list_post_*.txt``
were captured during Phase 2 by running ``dc_select_network.sh --list``.
Phase 2.5 must produce byte-identical formatted output for the parts we
control (header, registry table, summary, docker-network table, free-
range stanza, pruning hints).

Two pieces of the fixture are intentionally NOT compared:

  1. The ``Worktree parent (conventional):`` line — bash and Python both
     compute this from the caller's cwd, so the value can vary between
     captures (the fixture's value is the worktree the user happened to
     run it from). We replace it with a placeholder before comparing.

  2. The ``Docker networks in 172.[16-31].0.0/16:`` block — depends on
     live ``docker network ls`` state. We check the *header lines* match
     (same column widths, same labels) but treat the *body rows* as
     opaque; a separate end-to-end smoke covers the live block.

  3. The ``Registry: <path>`` line — depends on $HOME / $MCK_DEVC_REGISTRY_DIR.

What we DO byte-match:

  - Registry table header + dashes.
  - Per-row formatting: branch_dir/prefix/status/worktree column widths.
  - Summary line.
  - Free-range stanza.
  - Pruning info block.
"""

from __future__ import annotations

import os
import re
import tempfile
import unittest
from io import StringIO
from pathlib import Path
from unittest.mock import patch

from _common import FakePopenFactory, fake_which  # noqa: E402

from wt_ctl.domains.network import (  # noqa: E402
    Registry,
    _RegistryRow,
    stack_params,
)
from wt_ctl.runner import Runner  # noqa: E402


_FIXTURES = Path(__file__).resolve().parent / "fixtures"


# ---------------------------------------------------------------------------
# parsing the bash fixture so we can synthesize a matching registry
# ---------------------------------------------------------------------------

def _parse_fixture(text: str) -> tuple[list[tuple[str, int, str]], str, str]:
    """Return (rows, registry_path, worktree_parent)."""
    rows: list[tuple[str, int, str]] = []
    in_table = False
    registry_path = ""
    worktree_parent = ""
    for raw in text.splitlines():
        if raw.startswith("Registry: "):
            registry_path = raw[len("Registry: "):]
            continue
        if raw.startswith("Worktree parent (conventional): "):
            worktree_parent = raw[len("Worktree parent (conventional): "):]
            continue
        if re.match(r"^BRANCH_DIR\s+PREFIX\s+STATUS\s+WORKTREE\s*$", raw):
            in_table = True
            continue
        if in_table and raw.startswith("---"):
            continue
        if in_table:
            if not raw.strip() or raw.startswith("Summary:"):
                in_table = False
                continue
            parts = raw.split()
            if len(parts) >= 3:
                bd = parts[0]
                try:
                    pref = int(parts[1])
                except ValueError:
                    continue
                status = parts[2]
                rows.append((bd, pref, status))
    return rows, registry_path, worktree_parent


def _normalize(text: str) -> str:
    """Replace dynamic fields and the docker-networks body block with
    placeholders so we can compare just the parts Phase 2.5 controls.

    Active-row worktree paths look like ``<parent>/<branch_dir>``; the
    parent prefix varies between fixture-time and test-time. We rewrite
    the path column to ``<WT>/<branch_dir>`` so column widths and the
    branch_dir spelling are still load-bearing.

    The /23-expansion adds two pieces the legacy fixture doesn't have:

      - A stack-params extension table (``BRANCH_DIR  N  X  Y_BASE  ...``)
        immediately after the Summary line. We drop it entirely on the
        actual side.
      - The ``Free range:`` line gained a parenthetical about the new
        scheme. We collapse it to a placeholder so both sides match.
    """
    out_lines: list[str] = []
    in_registry_table = False
    in_docker_body = False
    in_stack_params = False
    for line in text.splitlines():
        if line.startswith("Registry: "):
            out_lines.append("Registry: <PATH>")
            continue
        if line.startswith("Worktree parent (conventional): "):
            out_lines.append("Worktree parent (conventional): <PARENT>")
            continue
        # Stack-params extension table — drop completely on either side.
        if re.match(r"^BRANCH_DIR\s+N\s+X\s+Y_BASE\s+Y_VIP\s+PORT\s+SCHEME\s*$", line):
            in_stack_params = True
            continue
        if in_stack_params:
            if not line.strip():
                in_stack_params = False
                # collapse leading blank line that the table left behind
                continue
            continue
        if line.startswith("Free range: "):
            out_lines.append("Free range: <RANGE>")
            continue
        if re.match(r"^BRANCH_DIR\s+PREFIX\s+STATUS\s+WORKTREE\s*$", line):
            in_registry_table = True
            out_lines.append(line)
            continue
        if in_registry_table:
            if line.startswith("---"):
                out_lines.append(line)
                continue
            if not line.strip() or line.startswith("Summary:"):
                in_registry_table = False
                out_lines.append(line)
                continue
            # Active row: trailing column is an absolute path; rewrite to
            # `<WT>/<basename>` so we still verify the basename matches
            # the branch_dir column.
            m = re.match(r"^(\S+)(\s+\d+\s+\S+\s+)(.+)$", line)
            if m:
                bd, mid, wt_path = m.group(1), m.group(2), m.group(3)
                if wt_path.strip() == "(missing)":
                    out_lines.append(line)
                else:
                    out_lines.append(f"{bd}{mid}<WT>/{Path(wt_path.strip()).name}")
                    continue
            out_lines.append(line)
            continue
        if re.match(r"^Docker networks in 172\.\[\d+-\d+\]\.0\.0/16:$", line):
            out_lines.append(line)  # header line — keep verbatim
            in_docker_body = True
            continue
        if in_docker_body:
            # Header rows ("NETWORK ... PREFIX" + dashes) come first; keep
            # them. Body rows are dropped entirely (the count varies with
            # live docker state). The block ends at the next blank line.
            if line.startswith("NETWORK") or line.startswith("-------"):
                out_lines.append(line)
                continue
            if not line.strip():
                in_docker_body = False
                out_lines.append(line)
                continue
            # Skip body rows entirely.
            continue
        out_lines.append(line)
    return "\n".join(out_lines) + ("\n" if text.endswith("\n") else "")


class NetworkListFormatTests(unittest.TestCase):
    """Apply the registry's renderer to a fixture-derived state and assert
    the formatted output is byte-equal to the fixture (after normalizing
    dynamic fields).
    """

    def _check_fixture(self, fixture_name: str) -> None:
        fixture_text = (_FIXTURES / fixture_name).read_text()
        rows, _reg_path, worktree_parent_str = _parse_fixture(fixture_text)
        with tempfile.TemporaryDirectory() as td:
            wt_parent = Path(worktree_parent_str)
            with patch.dict(os.environ, {"MCK_DEVC_REGISTRY_DIR": td}):
                # Synthesize matching worktree dirs for the "active" rows;
                # leave "stale" rows without a directory.
                for bd, _pref, status in rows:
                    if status == "active" and wt_parent.is_dir():
                        # Only create dirs in tmp (don't pollute real fs);
                        # if the fixture's parent doesn't exist, fall back
                        # to a tmp parent and rewrite expectations later.
                        pass
                # Use a tmp worktree-parent so the test is hermetic. We'll
                # fix up the expected text to use the same path.
                tmp_parent = Path(td) / "mdb"
                tmp_parent.mkdir()
                for bd, _pref, status in rows:
                    if status == "active":
                        (tmp_parent / bd).mkdir()
                # Fake runner: docker network ls returns nothing -> docker
                # body is empty; the normalizer drops body rows anyway.
                runner = Runner(
                    popen_factory=FakePopenFactory(
                        mapping={
                            ("docker", "network", "ls", "--format", "{{.Name}}"):
                                ("", "", 0),
                        },
                        default=("", "", 0),
                    ),
                    which=fake_which,
                )
                reg = Registry(runner, worktree_parent=tmp_parent)
                # Fixtures are pre-expansion (all prefixes are 16..31, the
                # legacy /16 band), so synthesize them as legacy rows that
                # round-trip through the bare-int form.
                reg._write([
                    _RegistryRow(bd, stack_params(pref, scheme="legacy"))
                    for bd, pref, _ in rows
                ])

                rendered = reg.render_list(
                    repo_root=None,
                    script_self=str(wt_parent / "lsierant_devcontainer" /
                                    "scripts" / "dev" / "dc_select_network.sh"),
                )

        expected_norm = _normalize(fixture_text)
        # The normalizer also drops the dynamic registry path & worktree
        # parent on the actual side; rebuild the rendered text with the
        # same placeholders for a clean byte compare.
        actual_norm = _normalize(rendered)
        self.assertEqual(
            actual_norm, expected_norm,
            msg="format drift between bash fixture and Python renderer\n"
                f"--- expected (normalized) ---\n{expected_norm}\n"
                f"--- actual (normalized) ---\n{actual_norm}",
        )

    def test_post_delete_fixture(self) -> None:
        self._check_fixture("network_list_post_delete.txt")

    def test_post_create_fixture(self) -> None:
        self._check_fixture("network_list_post_create.txt")


if __name__ == "__main__":
    unittest.main()
