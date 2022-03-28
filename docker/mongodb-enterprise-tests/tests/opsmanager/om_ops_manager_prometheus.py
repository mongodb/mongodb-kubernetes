import time

from kubetester import MongoDB, create_secret, random_k8s_name
from kubetester.certs import create_mongodb_tls_certs
from kubetester.http import https_endpoint_is_reachable
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase, generic_replicaset
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark


def certs_for_prometheus(issuer: str, namespace: str, resource_name: str) -> str:
    secret_name = random_k8s_name(resource_name + "-") + "-cert"

    return create_mongodb_tls_certs(
        issuer,
        namespace,
        resource_name,
        secret_name,
    )


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )

    # TODO(om60): Change this version to point at OM60 when released.
    resource.set_version("5.9.0")
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["replicas"] = 1

    return resource.create()


@fixture(scope="module")
def sharded_cluster(
    ops_manager: MongoDBOpsManager, namespace: str, issuer: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster.yaml"),
        namespace=namespace,
    )
    prom_cert_secret = certs_for_prometheus(issuer, namespace, resource.name)

    create_secret(namespace, "cluster-secret", {"password": "cluster-prom-password"})

    resource["spec"]["prometheus"] = {
        "username": "prom-user",
        "passwordSecretRef": {
            "name": "cluster-secret",
        },
        "tlsSecretKeyRef": {
            "name": prom_cert_secret,
        },
    }
    del resource["spec"]["cloudManager"]
    resource.configure(ops_manager, namespace)

    yield resource.create()


@fixture(scope="module")
def replica_set(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    custom_mdb_version: str,
    issuer: str,
) -> MongoDB:

    create_secret(namespace, "rs-secret", {"password": "prom-password"})

    resource = generic_replicaset(
        namespace, "5.0.6", "replica-set-with-prom", ops_manager
    )

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
def test_prometheus_endpoint_works_on_every_pod_with_changed_username(
    replica_set: MongoDB, namespace: str
):
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
def test_prometheus_endpoint_works_on_every_pod_on_the_cluster(
    sharded_cluster: MongoDB, namespace: str
):
    """
    Checks that all of the Prometheus endpoints that we expect are up and listening.
    """

    auth = ("prom-user", "cluster-prom-password")
    name = sharded_cluster.name

    mongos_count = sharded_cluster["spec"]["mongosCount"]
    for idx in range(mongos_count):
        url = f"https://{name}-mongos-{idx}.{name}-svc.{namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)

    shard_count = sharded_cluster["spec"]["shardCount"]
    mongodbs_per_shard_count = sharded_cluster["spec"]["mongodsPerShardCount"]
    for shard in range(shard_count):
        for mongodb in range(mongodbs_per_shard_count):
            url = f"https://{name}-{shard}-{mongodb}.{name}-sh.{namespace}.svc.cluster.local:9216/metrics"
            assert https_endpoint_is_reachable(url, auth, tls_verify=False)

    config_server_count = sharded_cluster["spec"]["configServerCount"]
    for idx in range(config_server_count):
        url = f"https://{name}-config-{idx}.{name}-cs.{namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)
