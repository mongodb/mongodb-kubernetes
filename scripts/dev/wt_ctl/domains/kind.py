"""kind clusters of a worktree and the docker ``kind`` network."""

from __future__ import annotations

import json
import os
import re
from pathlib import Path
from typing import Optional

from ..errors import WtCtlError
from ..runner import Runner

# The first node image minor an older kind CLI cannot create (kubeadm fails mid-create), the first kind release
# expected to create it (not confirmed), and a node tag of the minor before it. kind v0.30.0 fails on 1.37.0 nodes.
_NODE_LIMITS = (((1, 37), (0, 31), "v1.36.1"),)


KUBECTL_TIMEOUT = 10
# The names recreate_kind_clusters.sh gives the clusters of a multi cluster worktree after its prefix.
MULTI_CLUSTERS = ("e2e-operator", "e2e-cluster-1", "e2e-cluster-2", "e2e-cluster-3", "kind")


def member_clusters(worktree: Path, prefix: str) -> list[str]:
    """The kind clusters of the worktree's topology, as ``create`` recorded it (multi cluster by default)."""
    marker = worktree / ".generated" / ".current_topology"
    single = marker.is_file() and marker.read_text().strip() == "single"
    return [f"{prefix}{name}" for name in (("kind",) if single else MULTI_CLUSTERS)]


def node_image_problem(kind_version: str, node_image: str) -> Optional[str]:
    """Why this kind CLI cannot create nodes from ``node_image`` and what to do, or None."""
    kind = re.search(r"\bv(\d+)\.(\d+)\.\d+", kind_version)
    node = re.search(r":v(\d+)\.(\d+)\.\d+(?:@|$)", node_image.strip())
    if not kind or not node:
        return None
    for first_bad, first_ok, fallback_tag in _NODE_LIMITS:
        if tuple(map(int, node.groups())) >= first_bad and tuple(map(int, kind.groups())) < first_ok:
            repo = node_image.strip().rsplit(":", 1)[0]
            return (
                f"kind {kind.group(0)} cannot create Kubernetes {first_bad[0]}.{first_bad[1]} nodes ({node_image}). "
                f"Upgrade kind (v{first_ok[0]}.{first_ok[1]} is expected to work) or run with "
                f"KIND_NODE_IMAGE={repo}:{fallback_tag}"
            )
    return None


class KindDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    def check_node_image(self, worktree: Path) -> None:
        """Fail before any cluster is deleted when the kind CLI is too old for the node image."""
        node_image = os.environ.get("KIND_NODE_IMAGE", "")
        if not node_image:
            try:
                versions = json.loads((worktree / "kubernetes-versions.json").read_text())
                node_image = f"{versions['kind']['images']['max']}:v{versions['kubernetes']['max']}"
            except (OSError, ValueError, KeyError):
                return
        problem = node_image_problem(self.runner.run(["kind", "version"]).stdout, node_image)
        if problem:
            raise WtCtlError(problem)

    def clusters(self, prefix: str) -> list[str]:
        """The kind clusters whose name starts with the worktree's prefix."""
        out = self.runner.run(["kind", "get", "clusters"]).stdout.split()
        return [name for name in out if name.startswith(prefix)]

    def unhealthy_reason(self, worktree: Path, cluster: str) -> Optional[str]:
        """Why ``cluster`` cannot be relied on, or None. A stopped node cannot be started again, so a cluster with
        one is as unusable as one whose API server is down or whose system pods crash."""
        argv = ["docker", "ps", "-a", "--filter", f"label=io.x-k8s.kind.cluster={cluster}"]
        nodes = self.runner.run([*argv, "--format", "{{.Names}} {{.State}}"], check=False).stdout.splitlines()
        if not nodes:
            return "no node container"
        for node in nodes:
            name, state = node.split()
            if state != "running":
                return f"node {name} is {state}"
        kubectl = ["kubectl", "--kubeconfig", str(worktree / ".generated" / "kind-kubeconfigs" / cluster)]
        kubectl += [f"--request-timeout={KUBECTL_TIMEOUT}s", "get", "-o", "json"]
        res = self.runner.run([*kubectl, "nodes"], check=False)
        if res.rc != 0:
            return f"the API server does not answer within {KUBECTL_TIMEOUT}s"
        for node in json.loads(res.stdout)["items"]:
            ready = [c for c in node["status"]["conditions"] if c["type"] == "Ready"]
            if not ready or ready[0]["status"] != "True":
                return f"node {node['metadata']['name']} is not Ready"
        res = self.runner.run([*kubectl, "pods", "-n", "kube-system"], check=False)
        if res.rc != 0:
            return f"the API server does not answer within {KUBECTL_TIMEOUT}s"
        for pod in json.loads(res.stdout)["items"]:
            name, status = pod["metadata"]["name"], pod["status"]
            if status["phase"] not in ("Running", "Succeeded"):
                return f"pod {name} in kube-system is {status['phase']}"
            for container in status.get("containerStatuses", []) + status.get("initContainerStatuses", []):
                reason = container.get("state", {}).get("waiting", {}).get("reason")
                if reason in ("CrashLoopBackOff", "Error"):
                    return f"container {container['name']} of pod {name} in kube-system is {reason}"
        return None

    def unhealthy(self, worktree: Path, prefix: str) -> Optional[str]:
        """The first problem of the member clusters of the worktree, with the cluster name, or None."""
        for cluster in member_clusters(worktree, prefix):
            reason = self.unhealthy_reason(worktree, cluster)
            if reason:
                return f"{cluster}: {reason}"
        return None

    def delete_clusters(self, prefix: str) -> None:
        for name in self.clusters(prefix):
            self.runner.stream(["kind", "delete", "cluster", "--name", name])

    def join_network(self, worktree: Path, project: str) -> None:
        """Put the devcontainer and its k8s-proxy on the ``kind`` network, which the Linux host reaches the
        apiservers through (their ports are bound to 127.0.0.1). A container already on it is skipped."""
        for service in ("devcontainer", "k8s-proxy"):
            container = self.runner.run(
                ["docker", "compose", "-p", project, "ps", "-q", service], cwd=worktree
            ).stdout.strip()
            if not container:
                raise WtCtlError(f"service {service} of {project} is not running")
            networks = self.runner.run(
                ["docker", "inspect", "-f", "{{range $n, $_ := .NetworkSettings.Networks}}{{$n}} {{end}}", container]
            ).stdout.split()
            if "kind" not in networks:
                self.runner.run(["docker", "network", "connect", "kind", container])
