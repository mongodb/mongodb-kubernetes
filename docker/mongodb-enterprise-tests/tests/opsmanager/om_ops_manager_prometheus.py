import time

from kubernetes import client
from kubetester import MongoDB, create_or_update_secret, random_k8s_name
from kubetester.certs import create_mongodb_tls_certs
from kubetester.http import https_endpoint_is_reachable
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase, generic_replicaset
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


def certs_for_prometheus(issuer: str, namespace: str, resource_name: str) -> str:
    secret_name = random_k8s_name(resource_name + "-") + "-cert"

    return create_mongodb_tls_certs(
        issuer,
        namespace,
        resource_name,
        secret_name,
    )


CONFIGURED_PROMETHEUS_PORT = 9999


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_appdb_version: str,
    custom_version: str,
    issuer: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["replicas"] = 1

    create_or_update_secret(namespace, "appdb-prom-secret", {"password": "prom-password"})

    prom_cert_secret = certs_for_prometheus(issuer, namespace, resource.name + "-db")
    resource["spec"]["applicationDatabase"]["prometheus"] = {
        "username": "prom-user",
        "passwordSecretRef": {"name": "appdb-prom-secret"},
        "tlsSecretKeyRef": {
            "name": prom_cert_secret,
        },
    }

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@fixture(scope="module")
def sharded_cluster(ops_manager: MongoDBOpsManager, namespace: str, issuer: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster.yaml"),
        namespace=namespace,
    )
    prom_cert_secret = certs_for_prometheus(issuer, namespace, resource.name)

    create_or_update_secret(namespace, "cluster-secret", {"password": "cluster-prom-password"})

    resource["spec"]["prometheus"] = {
        "username": "prom-user",
        "passwordSecretRef": {
            "name": "cluster-secret",
        },
        "tlsSecretKeyRef": {
            "name": prom_cert_secret,
        },
        "port": CONFIGURED_PROMETHEUS_PORT,
    }
    del resource["spec"]["cloudManager"]
    resource.configure(ops_manager, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))

    yield resource.create()


@fixture(scope="module")
def replica_set(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    custom_mdb_version: str,
    issuer: str,
) -> MongoDB:

    create_or_update_secret(namespace, "rs-secret", {"password": "prom-password"})

    resource = generic_replicaset(namespace, custom_mdb_version, "replica-set-with-prom", ops_manager)

    prom_cert_secret = certs_for_prometheus(issuer, namespace, resource.name)
    resource["spec"]["prometheus"] = {
        "username": "prom-user",
        "passwordSecretRef": {
            "name": "rs-secret",
        },
        "tlsSecretKeyRef": {
            "name": prom_cert_secret,
        },
    }
    yield resource.create()


@mark.e2e_om_ops_manager_prometheus
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_ops_manager_prometheus
def test_create_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_ops_manager_prometheus
def test_prometheus_endpoint_works_on_every_pod(replica_set: MongoDB, namespace: str):
    members = replica_set["spec"]["members"]
    name = replica_set.name

    auth = ("prom-user", "prom-password")

    for idx in range(members):
        member_url = f"https://{name}-{idx}.{name}-svc.{namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(member_url, auth, tls_verify=False)


@mark.e2e_om_ops_manager_prometheus
def test_prometheus_can_change_credentials(replica_set: MongoDB):
    replica_set["spec"]["prometheus"] = {"username": "prom-user-but-changed-this-time"}
    replica_set.update()

    # TODO: is the resource even being moved away from Running phase?
    time.sleep(20)
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_ops_manager_prometheus
def test_prometheus_endpoint_works_on_every_pod_with_changed_username(replica_set: MongoDB, namespace: str):
    members = replica_set["spec"]["members"]
    name = replica_set.name

    auth = ("prom-user-but-changed-this-time", "prom-password")

    for idx in range(members):
        member_url = f"https://{name}-{idx}.{name}-svc.{namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(member_url, auth, tls_verify=False)


@mark.e2e_om_ops_manager_prometheus
def test_create_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_ops_manager_prometheus
def test_prometheus_endpoint_works_on_every_pod_on_the_cluster(sharded_cluster: MongoDB, namespace: str):
    """
    Checks that all of the Prometheus endpoints that we expect are up and listening.
    """

    auth = ("prom-user", "cluster-prom-password")
    name = sharded_cluster.name

    port = sharded_cluster["spec"]["prometheus"]["port"]
    mongos_count = sharded_cluster["spec"]["mongosCount"]
    for idx in range(mongos_count):
        url = f"https://{name}-mongos-{idx}.{name}-svc.{namespace}.svc.cluster.local:{port}/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)

    shard_count = sharded_cluster["spec"]["shardCount"]
    mongodbs_per_shard_count = sharded_cluster["spec"]["mongodsPerShardCount"]
    for shard in range(shard_count):
        for mongodb in range(mongodbs_per_shard_count):
            url = f"https://{name}-{shard}-{mongodb}.{name}-sh.{namespace}.svc.cluster.local:{port}/metrics"
            assert https_endpoint_is_reachable(url, auth, tls_verify=False)

    config_server_count = sharded_cluster["spec"]["configServerCount"]
    for idx in range(config_server_count):
        url = f"https://{name}-config-{idx}.{name}-cs.{namespace}.svc.cluster.local:{port}/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)


@mark.e2e_om_ops_manager_prometheus
def test_sharded_cluster_service_has_been_updated_with_prometheus_port(replica_set: MongoDB, sharded_cluster: MongoDB):
    # Check that the service that belong to the Replica Set has the
    # the default Prometheus port.
    assert_mongodb_prometheus_port_exist(
        replica_set.name + "-svc",
        replica_set.namespace,
        port=9216,
    )

    # Checks that the Services that belong to the Sharded cluster have
    # the configured Prometheus port.
    assert_mongodb_prometheus_port_exist(
        sharded_cluster.name + "-svc",
        sharded_cluster.namespace,
        port=CONFIGURED_PROMETHEUS_PORT,
    )
    assert_mongodb_prometheus_port_exist(
        sharded_cluster.name + "-cs",
        sharded_cluster.namespace,
        port=CONFIGURED_PROMETHEUS_PORT,
    )
    assert_mongodb_prometheus_port_exist(
        sharded_cluster.name + "-sh",
        sharded_cluster.namespace,
        port=CONFIGURED_PROMETHEUS_PORT,
    )


@mark.e2e_om_ops_manager_prometheus
def test_prometheus_endpoint_works_on_every_pod_on_appdb(ops_manager: MongoDB):
    auth = ("prom-user", "prom-password")
    name = ops_manager.name + "-db"

    for idx in range(ops_manager["spec"]["applicationDatabase"]["members"]):
        url = f"https://{name}-{idx}.{name}-svc.{ops_manager.namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)

    assert_mongodb_prometheus_port_exist(name + "-svc", ops_manager.namespace, 9216)


def assert_mongodb_prometheus_port_exist(service_name: str, namespace: str, port: int):
    services = client.CoreV1Api().read_namespaced_service(name=service_name, namespace=namespace)
    assert len(services.spec.ports) == 2
    ports = ((p.name, p.port) for p in services.spec.ports)

    assert ("mongodb", 27017) in ports
    assert ("prometheus", port) in ports
