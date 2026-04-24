"""``KubeconfigDomain.refresh`` — three dispatch branches.

- EVG-host mode (.current-evg-host present): proxy-url is patched on the
  host file (loopback:PROXY_PORT) and the devc variant (EVG_HOST_PROXY).
- Local-kind (no pin, loopback server URL): devc variant gets server
  rewritten to host.docker.internal:<port>, TLS-skip flag set, CA stripped.
- BYOC (no pin, non-loopback server URL): devc variant is a verbatim copy.

In all branches, kfp registration is attempted when K8S_FWD_PROXY is set.
We monkey-patch the urlopen helper so the tests don't try to talk to a
real server.
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

        self.fake_kfp = FakeKfp()
        self._orig_patch = kubeconfig_module._patch_kfp
        kubeconfig_module._patch_kfp = self.fake_kfp
        self.addCleanup(lambda: setattr(kubeconfig_module, "_patch_kfp", self._orig_patch))

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
        """If a prior refresh from the old "suffix on disk" design left
        the on-disk host kubeconfig with suffixed names, the next
        refresh must normalise back to bare on disk while still
        suffixing the kfp PATCH."""
        # Seed the host file with already-suffixed names (the bad state).
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
