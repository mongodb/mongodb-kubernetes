"""``KubeconfigDomain.refresh`` — three dispatch branches.

- EVG-host mode (.current-evg-host present): proxy-url is patched on the
  host file (loopback:PROXY_PORT) and the devc variant (EVG_HOST_PROXY).
- Local-kind (no pin, loopback server URL): devc variant gets server
  rewritten to host.docker.internal:<port>, TLS-skip flag set, CA stripped.
- BYOC (no pin, non-loopback server URL): devc variant is a verbatim copy.

In all branches, kfp registration is attempted when K8S_FWD_PROXY is set.
"""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from typing import Any

import wt_ctl.domains.kubeconfig as kubeconfig_module  # noqa: E402
import yaml  # noqa: E402
from _common import FakePopenFactory  # noqa: F401  (sys.path side-effect)
from wt_ctl.domains.kubeconfig import KubeconfigDomain  # noqa: E402
from wt_ctl.runner import Runner  # noqa: E402

_KIND_KUBECONFIG = """\
apiVersion: v1
kind: Config
current-context: kind-foo
clusters:
- name: kind-foo
  cluster:
    server: https://127.0.0.1:54321
    certificate-authority-data: BASE64_CA_BLOB
contexts:
- name: kind-foo
  context:
    cluster: kind-foo
    user: kind-foo
users:
- name: kind-foo
  user:
    client-certificate-data: BLOB
    client-key-data: BLOB
"""


_GKE_KUBECONFIG = """\
apiVersion: v1
kind: Config
current-context: gke_proj_zone_cluster
clusters:
- name: gke_proj_zone_cluster
  cluster:
    server: https://203.0.113.42
    certificate-authority-data: BASE64_CA_BLOB
contexts:
- name: gke_proj_zone_cluster
  context:
    cluster: gke_proj_zone_cluster
    user: gke_proj_zone_cluster
users: []
"""


_MC_KUBECONFIG = """\
apiVersion: v1
kind: Config
current-context: kind-e2e-cluster-1
clusters:
- name: kind-e2e-cluster-1
  cluster:
    server: https://127.0.0.1:37737
    certificate-authority-data: BASE64_CA_BLOB
- name: kind-e2e-cluster-2
  cluster:
    server: https://127.0.0.1:36145
    certificate-authority-data: BASE64_CA_BLOB
contexts:
- name: kind-e2e-cluster-1
  context:
    cluster: kind-e2e-cluster-1
    user: kind-e2e-cluster-1
users: []
"""


class FakeKfp:
    """Records every PATCH attempt instead of doing real HTTP."""

    def __init__(self) -> None:
        self.calls: list[tuple[str, bytes]] = []
        self.success = True

    def __call__(self, url: str, body: bytes, log: Any) -> bool:
        self.calls.append((url, body))
        log(f"FAKE-PATCH {url} bytes={len(body)}")
        return self.success

    def last_body_yaml(self) -> dict:
        """Parse the most recent PATCH body as YAML."""
        return yaml.safe_load(self.calls[-1][1])


class KubeconfigRefreshTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp_obj = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp_obj.cleanup)
        self.wt = Path(self.tmp_obj.name)
        (self.wt / ".generated").mkdir(parents=True)
        self.host_kc = self.wt / ".generated" / "current.kubeconfig"
        self.devc_kc = self.wt / ".generated" / "current.devc.kubeconfig"
        self.evg_pin = self.wt / ".generated" / ".current-evg-host"
        self.mc_base = self.wt / ".generated" / "multicluster_kubeconfig"
        self.mc_devc = self.wt / ".generated" / "multicluster.devc.kubeconfig"

        self.fake_kfp = FakeKfp()
        self._orig_patch = kubeconfig_module._patch_kfp
        kubeconfig_module._patch_kfp = self.fake_kfp
        self.addCleanup(lambda: setattr(kubeconfig_module, "_patch_kfp", self._orig_patch))

        # Pin the devc-server resolution to the macOS branch (host.docker.internal)
        # by default so the local-kind assertions don't depend on the test host's
        # OS or a real docker. Linux-path tests override via _force_linux.
        self._set_platform("Darwin")

    def _set_platform(self, system: str) -> None:
        orig = kubeconfig_module.platform.system
        kubeconfig_module.platform.system = lambda: system
        self.addCleanup(lambda: setattr(kubeconfig_module.platform, "system", orig))

    def _force_linux(self, node_ip: dict[str, str] | None) -> None:
        """Switch to the Linux branch and stub kind-node-IP resolution.
        node_ip maps cluster-entry-name → IP; None entries simulate an
        unresolvable node (docker miss / in-devc)."""
        self._set_platform("Linux")
        orig = kubeconfig_module._kind_node_ip
        kubeconfig_module._kind_node_ip = lambda _runner, name: (node_ip or {}).get(name)
        self.addCleanup(lambda: setattr(kubeconfig_module, "_kind_node_ip", orig))

    def _refresh(self, **env_overrides: str) -> None:
        env = {"K8S_FWD_PROXY": "127.0.0.1:11616", **env_overrides}
        KubeconfigDomain(Runner()).refresh(self.wt, env=env, in_devc=False, emit=lambda _m: None)

    # ------------------------------------------------------------------
    # Local-kind branch (no pin, loopback server).
    # ------------------------------------------------------------------
    def test_local_kind_rewrites_devc_server_and_tls_skip(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self._refresh(CLUSTER_NAME="kind-foo")
        devc = yaml.safe_load(self.devc_kc.read_text())
        cluster = devc["clusters"][0]["cluster"]
        self.assertEqual(cluster["server"], "https://host.docker.internal:54321")
        self.assertIs(cluster["insecure-skip-tls-verify"], True)
        self.assertNotIn("certificate-authority-data", cluster)
        # host file still server=127.0.0.1, no proxy-url, current-context pinned.
        host = yaml.safe_load(self.host_kc.read_text())
        self.assertEqual(host["clusters"][0]["cluster"]["server"], "https://127.0.0.1:54321")
        self.assertNotIn("proxy-url", host["clusters"][0]["cluster"])
        self.assertEqual(host["current-context"], "kind-foo")

    def test_local_kind_linux_rewrites_devc_server_to_kind_node_ip(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self._force_linux({"kind-foo": "172.18.0.2"})
        self._refresh(CLUSTER_NAME="kind-foo")
        cluster = yaml.safe_load(self.devc_kc.read_text())["clusters"][0]["cluster"]
        self.assertEqual(cluster["server"], "https://172.18.0.2:6443")
        self.assertIs(cluster["insecure-skip-tls-verify"], True)

    def test_local_kind_linux_indevc_preserves_host_written_server(self) -> None:
        # Host-side wrote the node IP; the in-devc refresh (no docker → node IP
        # unresolvable) must keep it, not clobber back to host.docker.internal.
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.devc_kc.write_text(_KIND_KUBECONFIG.replace("https://127.0.0.1:54321", "https://172.18.0.2:6443"))
        self._force_linux(None)
        self._refresh(CLUSTER_NAME="kind-foo")
        cluster = yaml.safe_load(self.devc_kc.read_text())["clusters"][0]["cluster"]
        self.assertEqual(cluster["server"], "https://172.18.0.2:6443")

    def test_local_kind_registers_host_variant_on_host_side(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self._refresh()
        self.assertEqual(len(self.fake_kfp.calls), 1)
        url, body = self.fake_kfp.calls[0]
        self.assertEqual(url, "http://127.0.0.1:11616/kubeconfig")
        # host file's bytes — server is still 127.0.0.1.
        self.assertIn(b"127.0.0.1:54321", body)
        self.assertNotIn(b"host.docker.internal", body)

    def test_local_kind_no_suffix_when_proxy_port_unset(self) -> None:
        """No MCK_DEVC_PROXY_PORT → no in-flight suffix. Mirrors EVG-CI
        runs that don't have a devc stack."""
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self._refresh(CLUSTER_NAME="kind-foo")  # no MCK_DEVC_PROXY_PORT
        host = yaml.safe_load(self.host_kc.read_text())
        self.assertEqual(host["current-context"], "kind-foo")
        self.assertEqual(host["clusters"][0]["name"], "kind-foo")
        # PATCH body matches the on-disk file (no suffix transform).
        body = self.fake_kfp.last_body_yaml()
        self.assertEqual(body["current-context"], "kind-foo")
        self.assertEqual(body["clusters"][0]["name"], "kind-foo")

    def test_local_kind_suffix_in_flight_when_proxy_port_set(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self._refresh(CLUSTER_NAME="kind-foo", MCK_DEVC_PROXY_PORT="9152")
        host = yaml.safe_load(self.host_kc.read_text())
        devc = yaml.safe_load(self.devc_kc.read_text())
        # On-disk files: bare on both sides.
        self.assertEqual(host["current-context"], "kind-foo")
        self.assertEqual(host["clusters"][0]["name"], "kind-foo")
        self.assertEqual(devc["current-context"], "kind-foo")
        self.assertEqual(devc["clusters"][0]["name"], "kind-foo")
        self.assertEqual(
            devc["clusters"][0]["cluster"]["server"],
            "https://host.docker.internal:54321",
        )
        # Wire bytes (host-side PATCH): suffixed for kfp.
        body = self.fake_kfp.last_body_yaml()
        self.assertEqual(body["current-context"], "kind-foo-9152")
        self.assertEqual(body["clusters"][0]["name"], "kind-foo-9152")

    # ------------------------------------------------------------------
    # BYOC branch (no pin, non-loopback server).
    # ------------------------------------------------------------------
    def test_byoc_identity_copies_to_devc(self) -> None:
        self.host_kc.write_text(_GKE_KUBECONFIG)
        self._refresh(CLUSTER_NAME="gke_proj_zone_cluster")
        host = yaml.safe_load(self.host_kc.read_text())
        devc = yaml.safe_load(self.devc_kc.read_text())
        self.assertEqual(
            host["clusters"][0]["cluster"]["server"],
            "https://203.0.113.42",
        )
        self.assertEqual(
            devc["clusters"][0]["cluster"]["server"],
            "https://203.0.113.42",
        )
        # No TLS-skip / CA strip for BYOC (the cert is presumed valid).
        self.assertNotIn("insecure-skip-tls-verify", devc["clusters"][0]["cluster"])
        self.assertIn("certificate-authority-data", devc["clusters"][0]["cluster"])

    # ------------------------------------------------------------------
    # Multi-cluster two-flavor derivation from the bare base.
    # ------------------------------------------------------------------
    def test_evg_host_multicluster_two_flavors(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.mc_base.write_text(_MC_KUBECONFIG)
        self.evg_pin.write_text("my-evg-host")
        self._refresh(MCK_DEVC_PROXY_PORT="8000", EVG_HOST_PROXY="http://gost-proxy:8080")
        # Host flavor (base) — every member gets the loopback host proxy.
        base = yaml.safe_load(self.mc_base.read_text())
        self.assertEqual([c["cluster"]["proxy-url"] for c in base["clusters"]], ["http://127.0.0.1:8000"] * 2)
        # Devc flavor — every member gets EVG_HOST_PROXY; servers unchanged.
        devc = yaml.safe_load(self.mc_devc.read_text())
        self.assertEqual([c["cluster"]["proxy-url"] for c in devc["clusters"]], ["http://gost-proxy:8080"] * 2)
        self.assertEqual(devc["clusters"][0]["cluster"]["server"], "https://127.0.0.1:37737")

    def test_evg_host_no_multicluster_base_is_noop(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.evg_pin.write_text("my-evg-host")
        self._refresh(MCK_DEVC_PROXY_PORT="8000", EVG_HOST_PROXY="http://gost-proxy:8080")
        self.assertFalse(self.mc_base.exists())
        self.assertFalse(self.mc_devc.exists())

    def test_local_kind_multicluster_devc_host_docker_internal(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.mc_base.write_text(_MC_KUBECONFIG)
        self._refresh()
        # Host flavor: bare loopback (laptop kind reachable directly), no proxy.
        base = yaml.safe_load(self.mc_base.read_text())
        self.assertNotIn("proxy-url", base["clusters"][0]["cluster"])
        self.assertEqual(base["clusters"][0]["cluster"]["server"], "https://127.0.0.1:37737")
        # Devc flavor: per-member host.docker.internal rewrite + TLS skip.
        devc = yaml.safe_load(self.mc_devc.read_text())
        self.assertEqual(
            [c["cluster"]["server"] for c in devc["clusters"]],
            ["https://host.docker.internal:37737", "https://host.docker.internal:36145"],
        )

    def test_local_kind_multicluster_linux_per_member_node_ip(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.mc_base.write_text(_MC_KUBECONFIG)
        self._force_linux({"kind-e2e-cluster-1": "172.18.0.3", "kind-e2e-cluster-2": "172.18.0.4"})
        self._refresh()
        devc = yaml.safe_load(self.mc_devc.read_text())
        self.assertEqual(
            [c["cluster"]["server"] for c in devc["clusters"]],
            ["https://172.18.0.3:6443", "https://172.18.0.4:6443"],
        )
        self.assertIs(devc["clusters"][0]["cluster"]["insecure-skip-tls-verify"], True)
        self.assertNotIn("certificate-authority-data", devc["clusters"][0]["cluster"])

    def test_local_kind_multicluster_linux_rewrites_clusterip_secret_by_context(self) -> None:
        # The MC operator Secret carries per-cluster ClusterIPs (non-loopback).
        # On Linux they must be rewritten to node IPs by context name, not
        # skipped as "already-rewritten".
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.mc_base.write_text(
            _MC_KUBECONFIG.replace("https://127.0.0.1:37737", "https://10.97.0.1:443").replace(
                "https://127.0.0.1:36145", "https://10.98.0.1:443"
            )
        )
        self._force_linux({"kind-e2e-cluster-1": "172.18.0.3", "kind-e2e-cluster-2": "172.18.0.4"})
        self._refresh()
        devc = yaml.safe_load(self.mc_devc.read_text())
        self.assertEqual(
            [c["cluster"]["server"] for c in devc["clusters"]],
            ["https://172.18.0.3:6443", "https://172.18.0.4:6443"],
        )

    # ------------------------------------------------------------------
    # EVG-host branch (pin present).
    # ------------------------------------------------------------------
    def test_evg_host_patches_proxy_url_on_both_variants(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.evg_pin.write_text("my-evg-host")
        self._refresh(
            MCK_DEVC_PROXY_PORT="8000",
            EVG_HOST_PROXY="http://gost-proxy:8080",
            CLUSTER_NAME="kind-foo",
        )
        host = yaml.safe_load(self.host_kc.read_text())
        devc = yaml.safe_load(self.devc_kc.read_text())
        self.assertEqual(
            host["clusters"][0]["cluster"]["proxy-url"],
            "http://127.0.0.1:8000",
        )
        self.assertEqual(
            devc["clusters"][0]["cluster"]["proxy-url"],
            "http://gost-proxy:8080",
        )
        # On-disk files: bare everywhere. Variant context env vars
        # (CLUSTER_NAME / MEMBER_CLUSTERS / TEST_POD_CLUSTER) reference
        # bare names; bin/reset calls kubectl --context "${CLUSTER_NAME}"
        # and would fail against suffixed entries.
        self.assertEqual(host["current-context"], "kind-foo")
        self.assertEqual(host["clusters"][0]["name"], "kind-foo")
        self.assertEqual(devc["current-context"], "kind-foo")
        self.assertEqual(devc["clusters"][0]["name"], "kind-foo")
        # PATCH-to-kfp body: suffixed in-flight so the on-host kfp can
        # disambiguate concurrent worktrees.
        body = self.fake_kfp.last_body_yaml()
        self.assertEqual(body["current-context"], "kind-foo-8000")
        self.assertEqual(body["clusters"][0]["name"], "kind-foo-8000")
        self.assertEqual(body["contexts"][0]["name"], "kind-foo-8000")
        self.assertEqual(body["contexts"][0]["context"]["cluster"], "kind-foo-8000")
        self.assertEqual(body["contexts"][0]["context"]["user"], "kind-foo-8000")
        self.assertEqual(body["users"][0]["name"], "kind-foo-8000")

    def test_evg_host_devc_side_kfp_patch_stays_bare(self) -> None:
        """When refresh runs INSIDE the devc, the PATCH goes to the
        in-container k8s-proxy sidecar (single-tenant per worktree) — no
        suffix needed and no suffix applied."""
        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.evg_pin.write_text("my-evg-host")
        KubeconfigDomain(Runner()).refresh(
            self.wt,
            env={
                "K8S_FWD_PROXY": "172.21.0.10:80",
                "MCK_DEVC_PROXY_PORT": "8000",
                "EVG_HOST_PROXY": "http://gost-proxy:8080",
                "CLUSTER_NAME": "kind-foo",
            },
            in_devc=True,
            emit=lambda _m: None,
        )
        body = self.fake_kfp.last_body_yaml()
        self.assertEqual(body["current-context"], "kind-foo")
        self.assertEqual(body["clusters"][0]["name"], "kind-foo")

    def test_evg_host_rerun_normalises_stale_on_disk_suffix(self) -> None:
        """A host kubeconfig with suffixed on-disk names must be normalised
        back to bare on disk while the kfp PATCH stays suffixed."""
        suffixed = _KIND_KUBECONFIG.replace("kind-foo", "kind-foo-8000")
        self.host_kc.write_text(suffixed)
        self.evg_pin.write_text("my-evg-host")
        self._refresh(
            MCK_DEVC_PROXY_PORT="8000",
            EVG_HOST_PROXY="http://gost-proxy:8080",
            CLUSTER_NAME="kind-foo",
        )
        host = yaml.safe_load(self.host_kc.read_text())
        # Normalised back to bare on disk.
        self.assertEqual(host["current-context"], "kind-foo")
        self.assertEqual(host["clusters"][0]["name"], "kind-foo")
        # Wire bytes: suffixed.
        body = self.fake_kfp.last_body_yaml()
        self.assertEqual(body["current-context"], "kind-foo-8000")
        self.assertEqual(body["clusters"][0]["name"], "kind-foo-8000")

    def test_evg_host_missing_proxy_port_raises(self) -> None:
        from wt_ctl.errors import WtCtlError

        self.host_kc.write_text(_KIND_KUBECONFIG)
        self.evg_pin.write_text("my-evg-host")
        with self.assertRaises(WtCtlError):
            self._refresh()  # no MCK_DEVC_PROXY_PORT

    # ------------------------------------------------------------------
    # devc-side registration choice.
    # ------------------------------------------------------------------
    def test_local_kind_devc_side_registers_devc_variant(self) -> None:
        self.host_kc.write_text(_KIND_KUBECONFIG)
        KubeconfigDomain(Runner()).refresh(
            self.wt,
            env={"K8S_FWD_PROXY": "172.21.0.10:80", "CLUSTER_NAME": "kind-foo"},
            in_devc=True,
            emit=lambda _m: None,
        )
        self.assertEqual(len(self.fake_kfp.calls), 1)
        url, body = self.fake_kfp.calls[0]
        self.assertEqual(url, "http://172.21.0.10:80/kubeconfig")
        # devc variant bytes — server rewritten to host.docker.internal.
        self.assertIn(b"host.docker.internal:54321", body)


if __name__ == "__main__":
    unittest.main()
