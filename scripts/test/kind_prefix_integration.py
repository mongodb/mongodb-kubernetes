"""Two worktrees with different prefixes (ita-, itb-) keep kind clusters on one docker daemon. The worktrees are
two git worktrees of HEAD (needs a git checkout). Unit tests do not collect it.
Needs docker, kind, kubectl, a private-context, 10 GB, ~/.docker/config.json (nodes mount it) and an ECR login."""

import re
import shutil
import subprocess
import tempfile
import time
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]
CONTEXTS = Path("scripts/dev/contexts")
KUBE_CONFIG = Path.home() / ".kube" / "config"
A_CLUSTERS = [f"ita-{n}" for n in ["e2e-operator", "e2e-cluster-1", "e2e-cluster-2", "e2e-cluster-3", "kind"]]
CLUSTERS = A_CLUSTERS + ["itb-kind"]
HOME_KUBE_CONFIG_BEFORE = KUBE_CONFIG.read_bytes() if KUBE_CONFIG.exists() else None


def run(*cmd, cwd=None):
    p = subprocess.run(cmd, cwd=cwd, text=True, capture_output=True)
    assert p.returncode == 0, f"{cmd} failed:\n{p.stdout[-2000:]}\n{p.stderr[-2000:]}"
    return p.stdout.strip()


def container_id(cluster):
    return run("docker", "inspect", "-f", "{{.Id}}", f"{cluster}-control-plane")


def kubectl(worktree, context, *args):
    return run("kubectl", "--kubeconfig", worktree / ".generated/current.kubeconfig", "--context", context, *args)


@pytest.fixture(scope="module")
def worktrees():
    before = sorted(run("kind", "get", "clusters").split())
    assert not set(before) & set(CLUSTERS), "clusters of an earlier run exist, delete them first"
    pools = re.findall(r"^export KIND_METALLB_RANGE_.*$", (REPO / CONTEXTS / "root-context").read_text(), re.M)
    tmp = Path(tempfile.mkdtemp(prefix="kind-prefixes-"))
    a, b = tmp / "a", tmp / "b"
    try:
        multi, single = "e2e_multi_cluster_kind", "e2e_mdb_kind_ubi_cloudqa"
        for path, prefix, row, context in [(a, "ita-", 120, multi), (b, "itb-", 121, single)]:
            run("git", "-C", REPO, "worktree", "add", "--detach", path, "HEAD")
            shutil.copy(REPO / CONTEXTS / "private-context", path / CONTEXTS)
            ctx = f'export WORKTREE_PREFIX="{prefix}"\n' + "\n".join(pools).replace("172.18.255.", f"172.18.{row}.")
            (path / ".generated").mkdir(exist_ok=True)
            (path / ".generated" / "worktree-context").write_text(ctx + "\n")
            run("scripts/dev/switch_context.sh", context, cwd=path)
        run("scripts/dev/recreate_kind_cluster.sh", "kind", cwd=b)
        b_cluster = container_id("itb-kind")
        run("scripts/dev/recreate_kind_clusters.sh", cwd=a)  # also interconnects the clusters and installs Istio
        assert container_id("itb-kind") == b_cluster, "recreating worktree a replaced a cluster of worktree b"
        yield a, b
    finally:
        for name in CLUSTERS:
            subprocess.run(["kind", "delete", "cluster", "--name", name])
        shutil.rmtree(tmp, ignore_errors=True)
        subprocess.run(["git", "-C", REPO, "worktree", "prune"])
    assert sorted(run("kind", "get", "clusters").split()) == before


def lb_address(worktree, context):
    kubectl(worktree, context, "create", "service", "loadbalancer", "it", "--tcp=80:80")
    for _ in range(30):
        ip = kubectl(worktree, context, "get", "service", "it", "-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
        if ip:
            return ip
        time.sleep(2)
    return ""


def test_a_kubeconfig_holds_only_the_clusters_of_its_worktree_under_the_usual_names(worktrees):
    a, b = worktrees
    contexts = ["config", "get-contexts", "-o", "name"]
    assert sorted(kubectl(a, "kind-kind", *contexts).split()) == sorted(c.replace("ita-", "kind-") for c in A_CLUSTERS)
    assert kubectl(b, "kind-kind", *contexts) == "kind-kind"
    assert kubectl(a, "kind-e2e-cluster-1", "get", "nodes", "-o", "name") == "node/ita-e2e-cluster-1-control-plane"


def test_kubeconfigs_stay_in_the_checkouts_and_out_of_the_home_directory(worktrees):
    a, b = worktrees
    kubeconfigs = [sorted(p.name for p in (s / ".generated/kind-kubeconfigs").iterdir()) for s in (a, b)]
    assert kubeconfigs == [sorted(A_CLUSTERS), ["itb-kind"]]
    assert (KUBE_CONFIG.read_bytes() if KUBE_CONFIG.exists() else None) == HOME_KUBE_CONFIG_BEFORE
    assert not list(KUBE_CONFIG.parent.glob("it[ab]-*"))


def test_load_balancer_addresses_come_from_the_pool_of_the_worktree(worktrees):
    a, b = worktrees
    assert re.fullmatch(r"172\.18\.120\.21\d", lb_address(a, "kind-e2e-cluster-1"))
    assert re.fullmatch(r"172\.18\.121\.(2[0-4]\d|250)", lb_address(b, "kind-kind"))
