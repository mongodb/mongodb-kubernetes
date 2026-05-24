"""Renders fixture state into the expected key-value text."""

from __future__ import annotations

import unittest

from _common import FakePopenFactory  # noqa: E402  (path side-effect only)
from wt_ctl.display import render_status, render_status_all  # noqa: E402
from wt_ctl.state import (  # noqa: E402
    DevcState,
    EvgHostState,
    GitState,
    GlobalStatus,
    KubeconfigState,
    NetState,
    OmState,
    OrphanRegistration,
    WorktreeRow,
    WorktreeStatus,
)


def _fixture() -> WorktreeStatus:
    return WorktreeStatus(
        worktree_dir="lsierant_devcontainer",
        worktree_path="/Users/x/mdb/lsierant_devcontainer",
        git=GitState(
            branch="lsierant/devcontainer",
            clean=True,
            ahead=0,
            behind=0,
            upstream="origin/lsierant/devcontainer",
        ),
        context="e2e_smoke",
        devc=DevcState(
            project="lsierant_devcontainer_devcontainer",
            state="running",
            services_running=4,
            services_total=4,
            image_age="2h",
        ),
        network=NetState(
            prefix=28,
            subnet="172.28.0.0/16",
            namespace="mck-devc-28-mongodb-test",
            gost_proxy="http://127.0.0.1:8028",
            registered=True,
            orphans=0,
        ),
        evg=EvgHostState(
            name="lsierant_devcontainer",
            id="i-0abc",
            status="running",
            host_name="ec2-1-2-3-4.compute-1.amazonaws.com",
            expires_in="3h12m",
            ssh="ssh ubuntu@ec2-1-2-3-4.compute-1.amazonaws.com",
        ),
        kubeconfig=KubeconfigState(
            path="/x/.generated/current.kubeconfig",
            last_patch="12m_ago",
            kfp_registered=True,
        ),
        om=OmState(
            namespace="ls-28",
            project_count=2,
            scope="ls-28, ls-28-*",
        ),
        next_hints=["wt-ctl attach"],
    )


class StatusRenderTests(unittest.TestCase):
    def test_keys_present(self) -> None:
        out = render_status(_fixture(), color="never")
        for k in (
            "worktree",
            "path",
            "context",
            "devc",
            "network",
            "namespace",
            "gost-proxy",
            "evg host",
            "kubeconfig",
            "om",
            "next",
        ):
            self.assertIn(k, out, f"missing key {k!r} in:\n{out}")
        self.assertIn("172.28.0.0/16", out)
        self.assertIn("http://127.0.0.1:8028", out)

    def test_no_devc_renders_clean(self) -> None:
        snap = _fixture()
        snap.devc = None
        out = render_status(snap, color="never")
        self.assertIn("no compose stack found", out)


class StatusAllRenderTests(unittest.TestCase):
    def test_basic_table(self) -> None:
        rows = [
            WorktreeRow(
                worktree="alpha",
                branch="topic/a",
                prefix=20,
                namespace="ns-20",
                devc_state="running",
                evg_name="alpha",
                evg_status="running",
                evg_expires="2h",
            ),
            WorktreeRow(
                worktree="beta",
                branch="topic/b",
                prefix=21,
                namespace="ns-21",
                devc_state="exited",
                evg_name=None,
                evg_status=None,
                evg_expires=None,
                prunable=True,
                prunable_reason="devc-exited",
                delete_cmd="wt-ctl delete topic/b",
            ),
        ]
        orphans = [
            OrphanRegistration(
                branch_dir="ghost",
                prefix=22,
                release_cmd="wt-ctl network release ghost",
            ),
        ]
        out = render_status_all(GlobalStatus(rows=rows, orphans=orphans), color="never")
        self.assertIn("WORKTREE", out)
        self.assertIn("alpha", out)
        self.assertIn("beta", out)
        self.assertIn("prunable", out)
        self.assertIn("orphan registrations", out)
        self.assertIn("ghost", out)


if __name__ == "__main__":
    unittest.main()
