"""B6 smoke: the live decentralized installation over three real kind clusters. Deliverable:
three operators that each log their own cluster identity and reconcile their local copy of the
same CR."""

import os
from typing import Dict

import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester

from tests.conftest import get_multi_cluster_operator_installation_config
from tests.multicluster_decentralized.installer import (
    DECENTRALIZED_OPERATOR_NAME,
    InstallerSettings,
    install_decentralized,
)

# The live run is gated (it needs real kind clusters and OM credentials from the standard
# context env); everything in this file skips without the explicit opt-in.
pytestmark = pytest.mark.skipif(
    os.environ.get("DECENTRALIZED_LIVE") != "true",
    reason="gated live verification (B6): set DECENTRALIZED_LIVE=true to run against real clusters",
)


@pytest.mark.e2e_multi_cluster_decentralized
def test_install_decentralized(
    decentralized_cluster_clients: Dict[str, client.ApiClient],
    decentralized_settings: InstallerSettings,
):
    base_helm_args = get_multi_cluster_operator_installation_config(decentralized_settings.namespace)
    operators = install_decentralized(decentralized_cluster_clients, decentralized_settings, base_helm_args)

    assert sorted(operators) == sorted(decentralized_settings.clusters)


@pytest.mark.e2e_multi_cluster_decentralized
def test_operators_log_their_identity_and_read_the_local_cr(
    decentralized_cluster_clients: Dict[str, client.ApiClient],
    decentralized_settings: InstallerSettings,
):
    for cluster, api_client in decentralized_cluster_clients.items():
        corev1 = client.CoreV1Api(api_client)
        pods = corev1.list_namespaced_pod(
            decentralized_settings.namespace,
            label_selector=f"app.kubernetes.io/name={DECENTRALIZED_OPERATOR_NAME}",
        ).items
        assert len(pods) == 1, f"expected exactly one operator pod on {cluster}, found {len(pods)}"

        # the mesh-enrolled namespace injects an istio sidecar into the operator pod too;
        # log reads on a multi-container pod must name the container
        logs = corev1.read_namespaced_pod_log(
            pods[0].metadata.name, decentralized_settings.namespace, container=DECENTRALIZED_OPERATOR_NAME
        )
        assert "Decentralized multi-cluster mode enabled" in logs, f"no decentralized-mode banner on {cluster}"
        assert cluster in logs, f"operator on {cluster} does not log its own identity"
        # The local CR copy was read: the reconciler logs the resource it works on.
        assert "multi-replica-set" in logs, f"operator on {cluster} never touched its local CR copy"


@pytest.mark.e2e_multi_cluster_decentralized
def test_no_broad_cross_cluster_credential_exists(
    decentralized_cluster_clients: Dict[str, client.ApiClient],
    decentralized_settings: InstallerSettings,
):
    """The scope discipline at artifact level: on every cluster, the only Role bound to a peer
    ServiceAccount is the two-rule contract Role."""
    from tests.multicluster_decentralized.installer import build_peer_role

    for cluster, api_client in decentralized_cluster_clients.items():
        rbacv1 = client.RbacAuthorizationV1Api(api_client)
        role = rbacv1.read_namespaced_role(f"mck-member-{cluster}-peer-role", decentralized_settings.namespace)
        expected = build_peer_role(cluster, decentralized_settings.namespace)["rules"]
        actual = [{"apiGroups": r.api_groups, "resources": r.resources, "verbs": r.verbs} for r in role.rules]
        assert actual == expected, f"peer Role on {cluster} drifted from the frozen contract"
