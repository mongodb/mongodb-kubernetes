import ipaddress
from pathlib import Path

import pytest
from wt_ctl.domains.network import (
    INDEX_HI,
    compose_subnet,
    parse_subnets,
    worktree_params,
    worktree_values,
    write_worktree_context,
)
from wt_ctl.errors import WorktreeIndexError, WtCtlError

ROOT_CONTEXT = Path(__file__).resolve().parents[2] / "contexts" / "root-context"


def test_worktree_values_move_the_pools_of_root_context_to_the_row_of_the_index():
    values = worktree_values(0, ROOT_CONTEXT.read_text())

    assert values["WORKTREE_PREFIX"] == "devc0-"
    assert values["KIND_METALLB_RANGE_CLUSTER_1"] == "172.18.128.210-172.18.128.219"
    assert len([name for name in values if name.startswith("KIND_METALLB_RANGE_")]) == 6


@pytest.mark.parametrize("index, row", [(5, "133"), (INDEX_HI, "254")])
def test_worktree_values_use_row_128_plus_index(index, row):
    values = worktree_values(index, ROOT_CONTEXT.read_text())

    assert values["WORKTREE_PREFIX"] == f"devc{index}-"
    assert values["KIND_METALLB_RANGE_SINGLE"].startswith(f"172.18.{row}.")


def test_worktree_values_refuse_an_index_without_a_row():
    with pytest.raises(WorktreeIndexError, match=str(INDEX_HI)):
        worktree_values(INDEX_HI + 1, ROOT_CONTEXT.read_text())


@pytest.mark.parametrize("evg_host_name, expected_lines", [("", 7), ("h1", 8)])
def test_worktree_context_has_only_literal_export_lines(tmp_path, evg_host_name, expected_lines):
    path = tmp_path / "worktree-context"

    write_worktree_context(path, worktree_values(5, ROOT_CONTEXT.read_text()), evg_host_name)

    lines = [line for line in path.read_text().splitlines() if not line.startswith("#")]
    assert len(lines) == expected_lines
    assert all(line.startswith("export ") and line.endswith('"') for line in lines)
    assert 'export WORKTREE_PREFIX="devc5-"' in lines
    assert ('export EVG_HOST_NAME="h1"' in lines) == bool(evg_host_name)
    assert not any("$" in line or "`" in line for line in lines)


def test_a_host_name_that_a_shell_would_expand_is_refused(tmp_path):
    with pytest.raises(WtCtlError, match="worktree-context file"):
        write_worktree_context(tmp_path / "worktree-context", worktree_values(5, ROOT_CONTEXT.read_text()), 'h"$(id)')

    assert not (tmp_path / "worktree-context").exists()


def test_compose_subnet_of_an_index_overlaps_a_network_that_holds_its_addresses():
    docker_output = "172.17.0.0/16 \n172.16.2.0/23 fc00::/64 \n\n172.18.0.0/16 "

    overlapping = [
        n for n in range(4) if any(compose_subnet(worktree_params(n)).overlaps(s) for s in parse_subnets(docker_output))
    ]

    assert overlapping == [1]


def test_parse_subnets_keeps_only_ipv4_subnets():
    assert parse_subnets("172.17.0.0/16 fc00::/64 not-a-subnet") == [ipaddress.ip_network("172.17.0.0/16")]
