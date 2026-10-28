"""Two wt-ctl worktrees on one docker daemon: create both, delete one, the other stays up. Needs a git checkout (it
adds git worktrees of HEAD); the unit tests do not collect this file. Needs docker, kind, kubectl, the devcontainer
CLI, a private-context, about 14 GB, and an ECR login or KIND_NODE_IMAGE with that image pulled."""

import hashlib
import os
import re
import shutil
import subprocess
import sys
import tempfile
from collections import namedtuple
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "scripts" / "dev"))
from wt_ctl.domains.kind import KindDomain  # noqa: E402
from wt_ctl.runner import Runner  # noqa: E402

CONTEXTS = Path("scripts/dev/contexts")
COMPOSE_OVERRIDES = Path(".devcontainer/compose.user.yml")
BRANCHES = ["it-a", "it-b"]
KUBE_CONFIG = Path.home() / ".kube" / "config"
Worktrees = namedtuple("Worktrees", "host a b home_before")  # host: the checkout `create` runs from
ENV = {k: v for k, v in os.environ.items() if k not in ("PROJECT_DIR", "KUBECONFIG")}
ENV["PATH"] = f"{Path(sys.executable).parent}{os.pathsep}{ENV['PATH']}"  # wt-ctl needs this python for PyYAML
if (Path(sys.prefix) / "bin" / "activate").is_file():
    ENV["PROJECT_VENV_PATH"] = sys.prefix  # the worktrees reuse this venv instead of building one each


def run(*cmd, cwd=None, check=True):
    p = subprocess.run([str(c) for c in cmd], cwd=cwd, env=ENV, text=True, capture_output=True)
    assert not check or p.returncode == 0, f"{cmd} failed:\n{p.stdout[-2000:]}\n{p.stderr[-3000:]}"
    return p.stdout.strip()


def wt_ctl(checkout, *args, check=True):
    return run(checkout / "scripts/dev/wt-ctl", "--quiet", *args, cwd=checkout, check=check)


def in_container(worktree, script):
    return wt_ctl(worktree, "attach", "--", "bash", "-lc", f"cd /workspace && {script}")


def worktree_context(worktree):
    return (worktree / ".generated" / "worktree-context").read_text()


def prefix(worktree):
    return re.search(r'WORKTREE_PREFIX="(devc\d+-)"', worktree_context(worktree)).group(1)


def clusters():
    return set(run("kind", "get", "clusters").split())


def node_id(worktree):
    return run("docker", "inspect", "-f", "{{.Id}}", f"{prefix(worktree)}kind-control-plane")


def project_containers(worktree, *flags):
    project = f"{worktree.name}_devcontainer"
    return run("docker", "ps", *flags, "-q", "--filter", f"label=com.docker.compose.project={project}")


def home_state():
    kube = hashlib.sha256(KUBE_CONFIG.read_bytes()).hexdigest() if KUBE_CONFIG.exists() else None
    return kube, sorted(p.name for p in Path.home().iterdir())


@pytest.fixture(scope="module")
def worktrees():
    before = clusters()
    assert not [c for c in before if c.startswith("devc")], "clusters of an earlier run exist, delete them first"
    tmp = Path(tempfile.mkdtemp(prefix="wt-ctl-worktrees-"))
    host = tmp / "host"  # the worktrees are created next to it
    try:
        run("git", "-C", REPO, "worktree", "add", "--detach", host, "HEAD")
        shutil.copy(REPO / CONTEXTS / "private-context", host / CONTEXTS)
        # `create` hands these on to the worktrees. On CI they hold EVR_TASK_ID and workdir, which the containers need
        # to run `make switch` against the private-context of an Evergreen host.
        if (REPO / COMPOSE_OVERRIDES).is_file():
            shutil.copy(REPO / COMPOSE_OVERRIDES, host / COMPOSE_OVERRIDES)
        run("scripts/dev/switch_context.sh", "e2e_mdb_kind_ubi_cloudqa", cwd=host)
        home_before = home_state()  # after the switch: on Evergreen it selects the context in ~/.kube/config
        for branch in BRANCHES:
            assert run("git", "-C", REPO, "branch", "--list", branch) == "", f"branch {branch} exists, delete it first"
            wt_ctl(host, "create", "--local-kind", "--single-cluster", branch)
        yield Worktrees(host, tmp / "it-a", tmp / "it-b", home_before)
    finally:
        for branch in reversed(BRANCHES):
            wt_ctl(host, "delete", "--all", "--force", branch, check=False)
        for name in sorted(c for c in clusters() - before if c.startswith("devc")):
            subprocess.run(["kind", "delete", "cluster", "--name", name])
        shutil.rmtree(tmp, ignore_errors=True)
        run("git", "-C", REPO, "worktree", "prune")
        for branch in BRANCHES:
            run("git", "-C", REPO, "branch", "-D", branch, check=False)


def test_each_worktree_has_its_own_prefix_and_its_own_load_balancer_row(worktrees):
    assert prefix(worktrees.a) != prefix(worktrees.b)
    for worktree in (worktrees.a, worktrees.b):
        row = 128 + int(re.search(r"devc(\d+)-", prefix(worktree)).group(1))
        assert len(re.findall(rf'="172\.18\.{row}\.\d+-172\.18\.{row}\.\d+"', worktree_context(worktree))) == 6
        assert re.search(rf'^NAMESPACE="?{prefix(worktree)}', (worktree / ".generated/context.env").read_text(), re.M)


def test_each_kubeconfig_in_the_checkout_reaches_only_the_cluster_of_its_worktree(worktrees):
    servers = []
    for worktree in (worktrees.a, worktrees.b):
        kubectl = ["kubectl", "--kubeconfig", worktree / ".generated/current.kubeconfig"]
        assert f"{prefix(worktree)}kind" in clusters()
        assert run(*kubectl, "config", "get-contexts", "-o", "name") == "kind-kind"
        assert run(*kubectl, "get", "nodes", "-o", "name") == f"node/{prefix(worktree)}kind-control-plane"
        servers.append(run(*kubectl, "config", "view", "-o", "jsonpath={..server}"))
    assert servers[0] != servers[1]


def test_the_container_of_each_worktree_reaches_the_node_of_its_worktree(worktrees):
    for worktree in (worktrees.a, worktrees.b):
        assert in_container(worktree, "kubectl get nodes -o name") == f"node/{prefix(worktree)}kind-control-plane"


def test_git_works_in_the_container_of_each_worktree_on_the_commit_of_the_host(worktrees):
    for worktree in (worktrees.a, worktrees.b):
        assert in_container(worktree, "git status --porcelain") == ""
        assert in_container(worktree, "git log -1 --format=%H") == run(
            "git", "-C", worktree, "log", "-1", "--format=%H"
        )
        assert in_container(worktree, "git config --global gc.worktreePruneExpire") == "never"


def test_the_health_check_passes_for_a_new_cluster_and_names_a_stopped_node(worktrees):
    kind, cluster = KindDomain(Runner()), f"{prefix(worktrees.a)}kind"
    assert kind.unhealthy_reason(worktrees.a, cluster) is None
    run("docker", "stop", f"{cluster}-control-plane")
    assert kind.unhealthy_reason(worktrees.a, cluster) == f"node {cluster}-control-plane is exited"


def test_deleting_one_worktree_leaves_the_other_one_running(worktrees):
    prefix_a, prefix_b, b_node = prefix(worktrees.a), prefix(worktrees.b), node_id(worktrees.b)
    wt_ctl(worktrees.host, "delete", "--all", "it-a")
    assert f"{prefix_a}kind" not in clusters()
    assert not worktrees.a.exists()
    assert project_containers(worktrees.a, "-a") == ""
    assert f"{prefix_b}kind" in clusters()
    assert node_id(worktrees.b) == b_node, "deleting worktree a replaced the cluster of worktree b"
    assert project_containers(worktrees.b) != ""


def test_no_worktree_wrote_into_the_home_directory(worktrees):
    kube_before, names_before = worktrees.home_before
    kube, names = home_state()
    assert kube == kube_before
    assert sorted(set(names) ^ set(names_before)) == [], "entries added to or removed from the home directory"
