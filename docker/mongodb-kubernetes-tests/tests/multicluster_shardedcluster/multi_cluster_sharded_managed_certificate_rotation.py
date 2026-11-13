"""Self-signed server CA rotation for a multi-cluster sharded cluster with operator-managed certificates.

Every component (mongos, config server, each shard) has its own server certificate, shared by the member
clusters that run it, but the whole resource has one server trust bundle, which the operator publishes in its
own cluster and copies into every member cluster. Rotating the server CA must widen that one bundle in every
member cluster, re-issue the server leaf of every component on the new root and roll the members in place;
pruning the previous CA must then reach every member cluster too. The client category works the same way and
is covered by the single-cluster sharded and the multi-cluster replica set rotation tests, so it is left out to
keep this module short.
"""

from typing import List

import kubernetes
from kubetester import find_fixture, read_secret, try_load
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import ensure_ent_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster.managedcert_rotation_mc_helper import (
    CA_BUNDLE_KEY,
    CLIENT,
    SERVER,
    assert_certificate_renewals_reported,
    assert_single_ca_bundle_per_category,
    bundle_snapshot,
    ca_bundle_name,
    ca_certificate_name,
    leaf_certificates,
    pem_latest_hash,
    pod_uids_in_clusters,
    read_bundle_data,
    wait_for_automation_config_ca_files,
    wait_for_automation_config_names_files,
    wait_for_bundle_replicated,
    wait_for_leaf_signed_by,
    wait_for_pem_secret_rolled_in_clusters,
    wait_for_prune_annotation_removed,
)
from tests.opsmanager.managedcert_helper import (
    begin_ca_rotation,
    bundle_fingerprints,
    wait_for_bundle_pruned,
    wait_for_bundle_rotation,
    wait_for_ca_rotated,
)
from tests.tls.managedcert_helper import (
    assert_reachable_with_client_certificate,
    client_certificate_user,
    read_certificate_status,
    request_prune_previous_ca,
)

MDB_RESOURCE = "sh-rotation"
SHARD_COUNT = 2
# Members of each component, per member cluster.
MONGOS_MEMBERS = [1, 1, 1]
CONFIG_SRV_MEMBERS = [1, 1, 1]
SHARD_MEMBERS = [1, 1, 1]

COMPONENTS = [f"{MDB_RESOURCE}-mongos", f"{MDB_RESOURCE}-config"] + [
    f"{MDB_RESOURCE}-{shard}" for shard in range(SHARD_COUNT)
]
SERVER_LEAVES = [f"{component}-managed-cert" for component in COMPONENTS]
AGENT_LEAF = f"{MDB_RESOURCE}-managed-agent-certs"
CLIENT_LEAVES = [f"{component}-managed-clusterfile" for component in COMPONENTS] + [AGENT_LEAF]

CONNECTIVITY_ATTEMPTS = 5


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    # cert-manager has to be installed before the operator starts, and the kubectl-mongodb setup tool has to
    # grant the cert-manager RBAC, see multi_cluster_sharded_managed_certificate.py.
    operator_installation_config = get_multi_cluster_operator_installation_config(namespace)
    operator_installation_config["security.managedCertificate.enabled"] = "true"
    return get_multi_cluster_operator(
        namespace,
        get_central_cluster_name(),
        operator_installation_config,
        get_central_cluster_client(),
        get_member_cluster_clients(),
        get_member_cluster_names(),
        create_managed_certificate_roles=True,
    )


@fixture(scope="module")
def sharded_cluster(
    cert_manager: str,
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE
    )
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["shardCount"] = SHARD_COUNT
    resource["spec"]["persistent"] = False
    resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), list(SHARD_MEMBERS))
    resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(
        get_member_cluster_names(), list(CONFIG_SRV_MEMBERS)
    )
    resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), list(MONGOS_MEMBERS))
    resource["spec"]["security"] = {
        "tls": {"enabled": True},
        "managedCertificate": {},
        "authentication": {"enabled": True, "modes": ["X509"], "internalCluster": "X509"},
    }
    resource.set_architecture_annotation()
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def app_user(sharded_cluster: MongoDB, namespace: str) -> MongoDBUser:
    return client_certificate_user(sharded_cluster, namespace)


@fixture(scope="module")
def rotation() -> dict:
    """The state each step of the rotation hands to the next one."""
    return {}


def _mongos_service_names() -> List[str]:
    """The service of every mongos pod: a multi-cluster sharded cluster has no single mongos service."""
    return [
        f"{MDB_RESOURCE}-mongos-{cluster_index}-{pod_index}-svc"
        for cluster_index, members in enumerate(MONGOS_MEMBERS)
        for pod_index in range(members)
    ]


def _assert_reachable(sharded_cluster: MongoDB, app_user: MongoDBUser, namespace: str):
    assert_reachable_with_client_certificate(
        sharded_cluster, app_user, namespace, service_names=_mongos_service_names(), attempts=CONNECTIVITY_ATTEMPTS
    )


def _pods(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> dict:
    return pod_uids_in_clusters(namespace, f"{MDB_RESOURCE}-", member_cluster_clients)


def _wait_for_automation_config_to_name_current_files(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    def hash_of(secret_name: str) -> str:
        return pem_latest_hash(namespace, f"{secret_name}-pem", member_cluster_clients)

    wait_for_automation_config_names_files(
        hash_of(AGENT_LEAF),
        {component: hash_of(f"{component}-managed-cert") for component in COMPONENTS},
        {component: hash_of(f"{component}-managed-clusterfile") for component in COMPONENTS},
    )


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
def test_sharded_cluster_running(sharded_cluster: MongoDB):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
def test_bundles_replicated_to_every_member_cluster(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    assert_single_ca_bundle_per_category(namespace, MDB_RESOURCE)
    for category in (SERVER, CLIENT):
        wait_for_bundle_replicated(namespace, ca_bundle_name(MDB_RESOURCE, category), member_cluster_clients)


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
def test_certificate_renewals_reported(sharded_cluster: MongoDB, namespace: str):
    assert_certificate_renewals_reported(sharded_cluster, namespace, SERVER_LEAVES, CLIENT_LEAVES)


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
@skip_if_local
def test_application_connects(sharded_cluster: MongoDB, app_user: MongoDBUser, namespace: str):
    # Skipped locally: the runner is outside the clusters and cannot resolve the mongos services.
    _assert_reachable(sharded_cluster, app_user, namespace)


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
def test_server_ca_rotation_replicates_the_widened_bundle(
    sharded_cluster: MongoDB, namespace: str, member_cluster_clients: List[MultiClusterClient], rotation: dict
):
    """The one widened server bundle, previousHash included, reaches every member cluster and every component."""
    rotation["pods"] = _pods(namespace, member_cluster_clients)
    rotation["leaves"] = leaf_certificates(namespace, SERVER_LEAVES)
    rotation["revisions"] = {
        leaf: read_certificate_status(namespace, leaf).get("revision", 0) for leaf in SERVER_LEAVES
    }
    rotation["pem_hashes"] = {
        leaf: pem_latest_hash(namespace, f"{leaf}-pem", member_cluster_clients) for leaf in SERVER_LEAVES
    }
    rotation["client_leaves"] = leaf_certificates(namespace, CLIENT_LEAVES)
    rotation["client_bundle"] = read_bundle_data(namespace, ca_bundle_name(MDB_RESOURCE, CLIENT))
    bundle_before = bundle_snapshot(namespace, MDB_RESOURCE, SERVER)
    assert bundle_before["latest_hash"], "the server bundle must have a latestHash"

    ca_name = ca_certificate_name(MDB_RESOURCE, SERVER)
    rotation["old_ca"] = begin_ca_rotation(namespace, ca_name)
    rotation["new_ca"] = wait_for_ca_rotated(namespace, ca_name, rotation["old_ca"])

    widened = wait_for_bundle_rotation(
        namespace,
        ca_bundle_name(MDB_RESOURCE, SERVER),
        CA_BUNDLE_KEY,
        bundle_before["latest_hash"],
        bundle_before["published_at"],
    )
    assert set(widened["certificates"]) == set(bundle_fingerprints(rotation["old_ca"])) | set(
        bundle_fingerprints(rotation["new_ca"])
    ), "the widened server bundle must hold the previous and the new CA"
    rotation["widened"] = widened
    assert_single_ca_bundle_per_category(namespace, MDB_RESOURCE)

    replicated = wait_for_bundle_replicated(namespace, ca_bundle_name(MDB_RESOURCE, SERVER), member_cluster_clients)
    assert replicated["latestHash"] == widened["latest_hash"]
    assert replicated["previousHash"] == bundle_before["latest_hash"]

    wait_for_automation_config_ca_files(
        sharded_cluster, widened["latest_hash"], rotation["client_bundle"]["latestHash"]
    )


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
def test_every_component_server_leaf_reissued_on_the_new_ca(
    sharded_cluster: MongoDB, namespace: str, member_cluster_clients: List[MultiClusterClient], rotation: dict
):
    """Every component's server leaf moves to the new root and reaches every member cluster; the members roll
    in place and the client leaves stay as they are."""
    for leaf in SERVER_LEAVES:
        new_leaf = wait_for_leaf_signed_by(namespace, leaf, rotation["leaves"][leaf], rotation["new_ca"])
        assert not cert_signed_by_bundle(new_leaf, rotation["old_ca"]), f"{leaf} must not be signed by the old CA"
        assert read_certificate_status(namespace, leaf).get("revision", 0) > rotation["revisions"][leaf]
        wait_for_pem_secret_rolled_in_clusters(
            namespace, f"{leaf}-pem", rotation["pem_hashes"][leaf], member_cluster_clients
        )

    assert (
        leaf_certificates(namespace, CLIENT_LEAVES) == rotation["client_leaves"]
    ), "a server CA rotation must not re-issue the client leaves"

    _wait_for_automation_config_to_name_current_files(namespace, member_cluster_clients)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)
    assert (
        _pods(namespace, member_cluster_clients) == rotation["pods"]
    ), "a CA rotation must not recreate the pods in any member cluster"

    # The previous CA stays trusted everywhere until it is pruned.
    replicated = wait_for_bundle_replicated(namespace, ca_bundle_name(MDB_RESOURCE, SERVER), member_cluster_clients)
    assert replicated["latestHash"] == rotation["widened"]["latest_hash"]


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
@skip_if_local
def test_application_connects_after_rotation(sharded_cluster: MongoDB, app_user: MongoDBUser, namespace: str):
    _assert_reachable(sharded_cluster, app_user, namespace)


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
def test_prune_previous_server_ca(
    sharded_cluster: MongoDB, namespace: str, member_cluster_clients: List[MultiClusterClient], rotation: dict
):
    """The narrowed server bundle reaches every member cluster and the operator removes the annotation."""
    request_prune_previous_ca(sharded_cluster, SERVER)
    wait_for_prune_annotation_removed(sharded_cluster)

    pruned = wait_for_bundle_pruned(namespace, ca_bundle_name(MDB_RESOURCE, SERVER), CA_BUNDLE_KEY, rotation["new_ca"])
    assert set(pruned["certificates"]) == set(bundle_fingerprints(rotation["new_ca"]))

    replicated = wait_for_bundle_replicated(namespace, ca_bundle_name(MDB_RESOURCE, SERVER), member_cluster_clients)
    assert replicated["latestHash"] == pruned["latest_hash"]
    assert replicated.get("previousHash") == pruned["previous_hash"], "every cluster must hold the same previousHash"
    assert (
        read_bundle_data(namespace, ca_bundle_name(MDB_RESOURCE, CLIENT)) == rotation["client_bundle"]
    ), "pruning the server CA must not change the client bundle"

    wait_for_automation_config_ca_files(sharded_cluster, pruned["latest_hash"], rotation["client_bundle"]["latestHash"])
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)
    assert _pods(namespace, member_cluster_clients) == rotation["pods"], "pruning must not recreate the pods"
    # The server leaves are all on the new root, which the narrowed bundle still trusts.
    for leaf in SERVER_LEAVES:
        assert cert_signed_by_bundle(read_secret(namespace, leaf)["tls.crt"], rotation["new_ca"])


@mark.e2e_managed_certificate_multi_cluster_sharded_rotation
@skip_if_local
def test_application_connects_after_prune(sharded_cluster: MongoDB, app_user: MongoDBUser, namespace: str):
    _assert_reachable(sharded_cluster, app_user, namespace)
