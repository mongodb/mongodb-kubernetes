"""Golden-output regression test for the cross-layer log analyzer.

Walks every fixture under ``testdata/fixtures/<scenario>/``, runs the
analyzer's render pipeline via ``generate_golden`` into a tmp dir, and
diffs the produced ``golden/*`` files against the committed ones.

Also validates that ``LogStore`` ingests every parser output and ends
up with the row counts recorded in ``row_counts.json``.

Run directly (no cluster needed):

  cd docker/mongodb-kubernetes-tests
  python -m pytest -q tests/common/search/test_analyzer_golden.py
"""

from __future__ import annotations

import difflib
import json
import os
import pathlib
import subprocess
import sys

import pytest

FIXTURES_DIR = pathlib.Path(__file__).resolve().parent.parent / "testdata" / "fixtures"
REPO_ROOT_TESTS = pathlib.Path(__file__).resolve().parents[4]  # docker/mongodb-kubernetes-tests/


def _fixture_scenarios() -> list[str]:
    if not FIXTURES_DIR.is_dir():
        return []
    return sorted(d.name for d in FIXTURES_DIR.iterdir() if d.is_dir() and (d / "metadata.json").is_file())


def _diff(committed: str, produced: str, label: str) -> str:
    diff = difflib.unified_diff(
        committed.splitlines(keepends=True),
        produced.splitlines(keepends=True),
        fromfile=f"{label} (committed golden)",
        tofile=f"{label} (produced)",
        n=3,
    )
    return "".join(diff)


@pytest.mark.parametrize("scenario", _fixture_scenarios())
def test_analyzer_golden(scenario: str, tmp_path: pathlib.Path) -> None:
    """Generated golden files must match the committed golden.

    The analyzer's only backend is the SQLite ``LogStore`` (see
    ``log_store.py``); ``generate_golden.py`` writes the canonical
    cursor-tree + unified-timeline outputs for each fixture.
    """
    fixture = FIXTURES_DIR / scenario
    committed_golden = fixture / "golden"
    assert committed_golden.is_dir(), f"missing committed golden for {scenario}"

    env = {**os.environ, "LANG": "C", "LC_ALL": "C"}

    # generate_golden writes the golden/ directory into the fixture dir.
    # Stage a fresh copy under tmp_path so we don't clobber the committed
    # files and so we can diff cleanly.
    shadow = tmp_path / scenario
    shadow.mkdir()
    # Symlink the fixture's log + metadata files, then add a writeable golden/.
    for f in fixture.iterdir():
        if f.name == "golden":
            continue
        (shadow / f.name).symlink_to(f.resolve())
    (shadow / "golden").mkdir()
    # Build a shadow FIXTURES path so generate_golden picks up our staged copy.
    fixtures_parent = shadow.parent

    env["MCK_ANALYZER_FIXTURES_DIR"] = str(fixtures_parent)
    subprocess.check_call(
        [
            sys.executable,
            "-m",
            "tests.common.search.testdata.generate_golden",
            "--scenario",
            scenario,
            "--timeline-max",
            "1000",
        ],
        cwd=str(REPO_ROOT_TESTS),
        env=env,
    )

    produced_golden = shadow / "golden"
    diffs: list[str] = []
    for committed_file in sorted(committed_golden.iterdir()):
        produced_file = produced_golden / committed_file.name
        if not produced_file.is_file():
            diffs.append(f"missing produced file: {produced_file}")
            continue
        committed_text = committed_file.read_text()
        produced_text = produced_file.read_text()
        if committed_text != produced_text:
            diffs.append(_diff(committed_text, produced_text, committed_file.name))
    # Also flag any extra files produced that aren't in the committed set.
    committed_names = {f.name for f in committed_golden.iterdir()}
    for produced_file in sorted(produced_golden.iterdir()):
        if produced_file.name not in committed_names:
            diffs.append(f"unexpected produced file: {produced_file.name}")

    assert not diffs, (
        f"analyzer output diverged from committed golden ({scenario}):\n"
        + "\n---\n".join(diffs[:5])
        + (f"\n... ({len(diffs) - 5} more)" if len(diffs) > 5 else "")
    )


@pytest.mark.parametrize("scenario", _fixture_scenarios())
def test_log_store_row_counts(scenario: str) -> None:
    """LogStore-side row counts match ``row_counts.json`` per fixture."""
    fixture = FIXTURES_DIR / scenario
    manifest = json.loads((fixture / "golden" / "row_counts.json").read_text())

    # Reuse generate_golden's parsing helpers.
    from tests.common.search.log_analyzer.analyzer import (
        build_stream_summaries,
        merge_envoy_access_log_into_streams,
        parse_client_wire_ops,
        parse_envoy_debug_log,
        read_envoy_access_log,
        read_mongod_commands,
        read_mongod_sessions,
        read_mongos_commands,
        read_mongos_remote_requests,
        read_mongot_interceptor_events,
    )
    from tests.common.search.log_analyzer.store import LogStore
    from tests.common.search.testdata import generate_golden as gg

    meta = json.loads((fixture / "metadata.json").read_text())
    namespace = meta["namespace"]
    paths = gg._group_logs(fixture, meta)
    mongos_paths = paths.get("mongos", [])
    mongod_paths = paths.get("mongod", [])
    mongot_paths = paths.get("mongot", [])
    envoy_paths = paths.get("envoy", [])

    mongos_cmds = read_mongos_commands(mongos_paths, namespace=namespace) if mongos_paths else []
    mongos_reqs = read_mongos_remote_requests(mongos_paths, namespace=namespace) if mongos_paths else []
    mongod_cmds = read_mongod_commands(mongod_paths, namespace=namespace) if mongod_paths else []
    mongod_sessions = read_mongod_sessions(mongod_paths, namespace=namespace) if mongod_paths else []
    envoy_streams = parse_envoy_debug_log(envoy_paths, namespace=namespace) if envoy_paths else []
    envoy_access = read_envoy_access_log(envoy_paths, namespace=namespace) if envoy_paths else []
    envoy_streams = merge_envoy_access_log_into_streams(envoy_streams, envoy_access)
    mongot_streams, mongot_batches = (
        build_stream_summaries(mongot_paths, namespace=namespace) if mongot_paths else ({}, [])
    )
    mongot_opens, mongot_cmds_log = (
        read_mongot_interceptor_events(mongot_paths, namespace=namespace) if mongot_paths else ([], [])
    )

    wire_ops_raw = gg._load_wire_ops(fixture / "client_wire_ops.jsonl")
    client_ops = parse_client_wire_ops(wire_ops_raw)

    store = LogStore()
    store.load_from_parsed_records(
        client_ops=client_ops,
        mongod_commands=mongod_cmds,
        mongod_sessions=mongod_sessions,
        mongos_commands=mongos_cmds,
        mongos_remote_requests=mongos_reqs,
        envoy_streams=envoy_streams,
        envoy_access=envoy_access,
        mongot_streams=mongot_streams,
        mongot_batches=mongot_batches,
        mongot_stream_opens=mongot_opens,
        mongot_cmds=mongot_cmds_log,
    )

    counts = store.row_counts()
    expected = manifest["rows"]
    # row_counts.json uses different keys for envoy_access:
    #   manifest "envoy_access" -> table "envoy_access_log"
    expected_renamed = {"envoy_access_log" if k == "envoy_access" else k: v for k, v in expected.items()}
    for table, want in expected_renamed.items():
        got = counts.get(table)
        assert got == want, f"{scenario}: row count {table}={got} (expected {want})"
