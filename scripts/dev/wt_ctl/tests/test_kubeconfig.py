import copy
from types import SimpleNamespace

import yaml
from wt_ctl.domains.kubeconfig import GOST_PROXY_URL, refresh, rewrite_for_devc


def kubeconfig(server="https://127.0.0.1:41234"):
    return {
        "clusters": [{"name": "kind-kind", "cluster": {"server": server, "certificate-authority-data": "LS0t"}}],
        "contexts": [{"name": "kind-kind", "context": {"cluster": "kind-kind", "user": "kind-kind"}}],
    }


def test_loopback_server_becomes_host_docker_internal_on_macos():
    result = rewrite_for_devc(kubeconfig())

    cluster = result["clusters"][0]["cluster"]
    assert cluster["server"] == "https://host.docker.internal:41234"
    assert cluster["insecure-skip-tls-verify"] is True
    assert "certificate-authority-data" not in cluster


def test_loopback_server_becomes_the_node_address_on_linux():
    result = rewrite_for_devc(kubeconfig(), node_ips={"kind-kind": "172.18.0.5"})

    cluster = result["clusters"][0]["cluster"]
    assert cluster["server"] == "https://172.18.0.5:6443"
    assert cluster["insecure-skip-tls-verify"] is True
    assert "certificate-authority-data" not in cluster


def test_server_that_is_not_loopback_is_left_as_it_is():
    original = kubeconfig("https://10.96.0.1:443")

    assert rewrite_for_devc(original) == original


def test_rewrite_does_not_change_its_input():
    original = kubeconfig()
    before = copy.deepcopy(original)

    rewrite_for_devc(original, node_ips={"kind-kind": "172.18.0.5"})

    assert original == before


class NoKindNodes:
    """A runner whose docker finds no kind node, so a Linux host rewrites as a Mac does."""

    def run(self, *args, **kwargs):
        return SimpleNamespace(rc=1, stdout="")


def worktree_with_kubeconfigs(tmp_path, evg=False):
    generated = tmp_path / ".generated"
    generated.mkdir()
    (generated / "current.kubeconfig").write_text(yaml.safe_dump(kubeconfig()))
    # what `kubectl mongodb multicluster setup` writes in the container: the CA of each cluster, no TLS opt-out
    operator_kubeconfig = kubeconfig("https://host.docker.internal:41234")
    (generated / "multicluster.devc.kubeconfig").write_text(yaml.safe_dump(operator_kubeconfig))
    if evg:
        (generated / ".current-evg-host").write_text("host")
    return generated, operator_kubeconfig


def clusters_of(path):
    return yaml.safe_load(path.read_text())["clusters"]


def test_refresh_keeps_the_operator_kubeconfig_with_the_ca_the_health_check_verifies(tmp_path):
    generated, operator_kubeconfig = worktree_with_kubeconfigs(tmp_path)

    refresh(NoKindNodes(), tmp_path, "devc1-", "kind-kind", 8080)

    assert yaml.safe_load((generated / "multicluster.devc.kubeconfig").read_text()) == operator_kubeconfig


def test_refresh_writes_the_container_kubeconfig_without_tls_verification(tmp_path):
    generated, _ = worktree_with_kubeconfigs(tmp_path)

    refresh(NoKindNodes(), tmp_path, "devc1-", "kind-kind", 8080)

    cluster = clusters_of(generated / "current.devc.kubeconfig")[0]["cluster"]
    assert cluster == {"server": "https://host.docker.internal:41234", "insecure-skip-tls-verify": True}


def test_refresh_on_an_evergreen_worktree_adds_the_proxy_to_the_operator_kubeconfig_and_keeps_its_ca(tmp_path):
    generated, _ = worktree_with_kubeconfigs(tmp_path, evg=True)

    refresh(NoKindNodes(), tmp_path, "devc1-", "kind-kind", 8080)

    cluster = clusters_of(generated / "multicluster.devc.kubeconfig")[0]["cluster"]
    assert cluster["proxy-url"] == GOST_PROXY_URL
    assert cluster["certificate-authority-data"] == "LS0t"
