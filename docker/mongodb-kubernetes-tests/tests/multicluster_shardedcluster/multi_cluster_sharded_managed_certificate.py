from typing import List

import kubernetes
from cryptography import x509
from kubetester import find_fixture, read_configmap, read_secret, try_load
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
from tests.tls.managedcert_helper import assert_reachable_with_client_certificate, client_certificate_user

MDB_RESOURCE = "sh"
SHARD_COUNT = 2
# Members of each component, per member cluster.
MONGOS_MEMBERS = [1, 2, 1]
CONFIG_SRV_MEMBERS = [2, 1, 1]
SHARD_MEMBERS = [2, 2, 1]


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    """Installs the operator with operator-managed certificates enabled, after cert-manager.

    The operator decides once, at startup, whether it may use cert-manager: the cert-manager CRDs
    have to be registered, the chart value has to be on, and it has to have the RBAC. This test sets
    all of that in its own operator install rather than for the whole variant. In multi-cluster mode
    the chart does not create the operator RBAC, the kubectl-mongodb setup tool does, so the
    cert-manager RBAC is asked for there. The cert_manager dependency forces cert-manager to be
    installed first. This overrides the fixture of the same name in tests/conftest.py for this
    module only.
    """
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
    # Fully operator-managed certificates: the customer only enables the feature and picks X509
    # internal auth. No user-provided certs and no CA ConfigMap anywhere.
    resource["spec"]["security"] = {
        "tls": {"enabled": True},
        "managedCertificate": {},
        "authentication": {"enabled": True, "modes": ["X509"], "internalCluster": "X509"},
    }
    resource.set_architecture_annotation()
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@fixture(scope="module")
def app_user(sharded_cluster: MongoDB, namespace: str) -> MongoDBUser:
    """The user an application connects as, with a client certificate the operator issues for it."""
    return client_certificate_user(sharded_cluster, namespace)


def _component_names() -> List[str]:
    """The name of every sharded component: mongos, the config server, and each shard."""
    return [f"{MDB_RESOURCE}-mongos", f"{MDB_RESOURCE}-config"] + [
        f"{MDB_RESOURCE}-{shard}" for shard in range(SHARD_COUNT)
    ]


def _members_per_cluster(component: str) -> List[int]:
    if component.endswith("-mongos"):
        return MONGOS_MEMBERS
    if component.endswith("-config"):
        return CONFIG_SRV_MEMBERS
    return SHARD_MEMBERS


def _mongos_service_names() -> List[str]:
    """The service of every mongos pod, which is what an application connects to.

    A multi-cluster sharded deployment has no single mongos service, each mongos pod has its own,
    named after the pod. The cluster index in the name is the position of the cluster in the member
    cluster list, the same numbering the operator uses.
    """
    return [
        f"{MDB_RESOURCE}-mongos-{cluster_index}-{pod_index}-svc"
        for cluster_index, members in enumerate(MONGOS_MEMBERS)
        for pod_index in range(members)
    ]


def _dns_sans(cert_pem: str) -> List[str]:
    cert = x509.load_pem_x509_certificate(cert_pem.encode())
    ext = cert.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    return sorted(ext.value.get_values_for_type(x509.DNSName))


def _cert_revision(central_cluster_client: kubernetes.client.ApiClient, namespace: str, name: str) -> int:
    # cert-manager bumps status.revision by one every time it issues (or reissues) the certificate,
    # so it tells us exactly how many times the cert was issued.
    cert = kubernetes.client.CustomObjectsApi(central_cluster_client).get_namespaced_custom_object(
        group="cert-manager.io", version="v1", namespace=namespace, plural="certificates", name=name
    )
    return cert.get("status", {}).get("revision", 0)


@mark.e2e_managed_certificate_multi_cluster_sharded
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_sharded
def test_sharded_cluster_running(sharded_cluster: MongoDB):
    # Reaching Running is the functional proof that every component in every member cluster presents
    # a valid managed certificate, authenticates with its managed clusterfile, and the agent
    # authenticates, with no user-provided certificates anywhere.
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_managed_certificate_multi_cluster_sharded
def test_component_certs_created_centrally(namespace: str):
    # One member (server) certificate and one clusterfile (client) certificate per component, all
    # created in the operator's cluster.
    for component in _component_names():
        for suffix in ("cert", "clusterfile"):
            secret_name = f"{component}-managed-{suffix}"
            secret = read_secret(namespace, secret_name)
            assert "tls.crt" in secret, f"{secret_name} missing tls.crt"
            assert "tls.key" in secret, f"{secret_name} missing tls.key"


@mark.e2e_managed_certificate_multi_cluster_sharded
def test_member_cert_sans_cover_every_member_cluster(namespace: str):
    # A component's certificate is shared by every member cluster that runs it, so its SANs are the
    # union across those clusters, and each name carries the cluster index and the pod index. A
    # missing member or a stray name fails the test.
    for component in _component_names():
        member_crt = read_secret(namespace, f"{component}-managed-cert")["tls.crt"]
        expected = sorted(
            f"{component}-{cluster_index}-{pod_index}-svc.{namespace}.svc.cluster.local"
            for cluster_index, members in enumerate(_members_per_cluster(component))
            for pod_index in range(members)
        )
        assert _dns_sans(member_crt) == expected, f"{component} SANs must cover every member of every cluster"


@mark.e2e_managed_certificate_multi_cluster_sharded
def test_ca_bundles_replicated_to_member_clusters(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    # The pods run in the member clusters and mount these ConfigMaps, but the operator only creates
    # them centrally. They must be copied into every member cluster, otherwise the pods have no CA to
    # trust and TLS fails.
    for ca_bundle, key in (
        (f"{MDB_RESOURCE}-managed-server-ca-bundle", "ca-pem"),
        (f"{MDB_RESOURCE}-managed-client-ca-bundle", "clusterca-pem"),
    ):
        central = read_configmap(namespace, ca_bundle)
        assert central.get(key, "").strip() != "", f"{ca_bundle} ({key}) missing/empty on the central cluster"
        for cluster_client in member_cluster_clients:
            cm = cluster_client.read_namespaced_config_map(ca_bundle, namespace)
            assert (cm.data or {}).get(
                key, ""
            ).strip() != "", f"{ca_bundle} ({key}) missing/empty in member cluster {cluster_client.cluster_name}"


@mark.e2e_managed_certificate_multi_cluster_sharded
def test_leaf_pem_secrets_present_in_every_member_cluster(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    # The operator derives a <secret>-pem secret from each managed leaf cert and the pods mount that.
    # Every component's secrets must exist in every member cluster for the pods to start with TLS.
    for component in _component_names():
        for suffix in ("cert", "clusterfile"):
            for cluster_client in member_cluster_clients:
                secret = cluster_client.core_v1_api().read_namespaced_secret(
                    f"{component}-managed-{suffix}-pem", namespace
                )
                assert (
                    secret.data
                ), f"{component}-managed-{suffix}-pem missing/empty in member cluster {cluster_client.cluster_name}"


@mark.e2e_managed_certificate_multi_cluster_sharded
@skip_if_local
def test_application_connects_with_client_certificate(sharded_cluster: MongoDB, app_user: MongoDBUser, namespace: str):
    # An application connects to mongos the way a real client does: it checks the server against the
    # server CA bundle and presents its own client certificate, which mongos checks against the client
    # CA bundle. Both bundles are created in the operator's cluster and copied to the member clusters,
    # so this is what proves the copies work as trust anchors, not just that they exist.
    # Skipped when running locally: the runner is outside the clusters and cannot resolve the mongos
    # services in the member clusters. The checks above, which read the objects, still run everywhere.
    assert_reachable_with_client_certificate(
        sharded_cluster, app_user, namespace, service_names=_mongos_service_names()
    )


@mark.e2e_managed_certificate_multi_cluster_sharded
def test_scale_up_and_down_shard_members(
    sharded_cluster: MongoDB,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    # Scale one shard cluster up by two members and then back down. The operator adds members one at a
    # time, so this exercises a multi-step scale. Each shard's certificate covers the union across its
    # member clusters and is grown to the target in one go, not reissued at every member step.

    shard_certs = [f"{MDB_RESOURCE}-{shard}-managed-cert" for shard in range(SHARD_COUNT)]
    # The config server and mongos are not touched by a shard scale, so their certificates must not
    # be issued again.
    untouched_certs = [f"{MDB_RESOURCE}-config-managed-cert", f"{MDB_RESOURCE}-mongos-managed-cert"]

    before = {name: _cert_revision(central_cluster_client, namespace, name) for name in shard_certs + untouched_certs}

    up: List[int | None] = [SHARD_MEMBERS[0] + 2, *SHARD_MEMBERS[1:]]
    sharded_cluster["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), up)
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)

    for name in shard_certs:
        assert (
            _cert_revision(central_cluster_client, namespace, name) == before[name] + 1
        ), f"{name} must be issued once for the whole scale up, not once per member step"
    for name in untouched_certs:
        assert (
            _cert_revision(central_cluster_client, namespace, name) == before[name]
        ), f"{name} must not be issued for a scale of the shards"

    down: List[int | None] = list(SHARD_MEMBERS)
    sharded_cluster["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), down)
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)

    for name in shard_certs:
        assert (
            _cert_revision(central_cluster_client, namespace, name) == before[name] + 1
        ), f"{name} must not be issued again on the way down: the SANs never shrink"


@mark.e2e_managed_certificate_multi_cluster_sharded
@skip_if_local
def test_application_connects_after_scale(sharded_cluster: MongoDB, app_user: MongoDBUser, namespace: str):
    # The scale reissued each shard's certificate, so the pods now run on a certificate issued after
    # the application's own. The connection has to keep working: the CAs did not change, and the
    # member clusters still hold the CA bundles that verify them. Skipped locally, like the
    # connection above.
    assert_reachable_with_client_certificate(
        sharded_cluster, app_user, namespace, service_names=_mongos_service_names()
    )
