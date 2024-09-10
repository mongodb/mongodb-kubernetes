from kubernetes import client
from kubetester import create_or_update, get_statefulset, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark

RESIZED_STORAGE_SIZE = "2Gi"

REPLICA_SET_NAME = "replica-set-resize"

# Note: This test can only be run in a cluster which uses - by default - a storageClass that is resizable; e.g., GKE
# For kind to work, you need to ensure that the resizable CSI driver has been installed, it will be installed
# Once you re-create your clusters


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        REPLICA_SET_NAME,
        f"prefix-{REPLICA_SET_NAME}-cert",
        replicas=3,
    )


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-pv-resize.yaml"),
        namespace=namespace,
        name=REPLICA_SET_NAME,
    )

    resource["spec"]["security"] = {}
    resource["spec"]["security"]["tls"] = {"ca": issuer_ca_configmap}
    # Setting security.certsSecretPrefix implicitly enables TLS
    resource["spec"]["security"]["certsSecretPrefix"] = "prefix"

    resource.set_version(custom_mdb_version)
    try_load(resource)
    return resource


@mark.e2e_replica_set_pv_resize
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    create_or_update(replica_set)
    replica_set.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_replica_set_pv_resize
def test_replica_set_resize_pvc_state_changes(replica_set: MongoDB):
    # Update the resource
    replica_set.load()
    replica_set["spec"]["podSpec"]["persistence"]["multiple"]["data"]["storage"] = RESIZED_STORAGE_SIZE
    replica_set["spec"]["podSpec"]["persistence"]["multiple"]["journal"]["storage"] = RESIZED_STORAGE_SIZE
    create_or_update(replica_set)
    replica_set.assert_reaches_phase(Phase.Pending, timeout=400)
    replica_set.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_replica_set_pv_resize
def test_replica_set_resize_finished(replica_set: MongoDB, namespace: str):
    sts = get_statefulset(namespace, REPLICA_SET_NAME)
    assert sts.spec.volume_claim_templates[0].spec.resources.requests["storage"] == RESIZED_STORAGE_SIZE

    first_data_pvc_name = "data-replica-set-resize-0"
    first_journal_pvc_name = "journal-replica-set-resize-0"
    first_logs_pvc_name = "logs-replica-set-resize-0"
    data_pvc = client.CoreV1Api().read_namespaced_persistent_volume_claim(first_data_pvc_name, namespace)
    assert data_pvc.status.capacity["storage"] == RESIZED_STORAGE_SIZE

    journal_pvc = client.CoreV1Api().read_namespaced_persistent_volume_claim(first_journal_pvc_name, namespace)
    assert journal_pvc.status.capacity["storage"] == RESIZED_STORAGE_SIZE

    initial_storage_size = "1Gi"
    logs_pvc = client.CoreV1Api().read_namespaced_persistent_volume_claim(first_logs_pvc_name, namespace)
    assert logs_pvc.status.capacity["storage"] == initial_storage_size


@mark.e2e_replica_set_pv_resize
def test_mdb_is_not_reachable_with_no_ssl(replica_set: MongoDB):
    replica_set.tester(use_ssl=False).assert_no_connection()


@mark.e2e_replica_set_pv_resize
def test_mdb_is_reachable_with_ssl(replica_set: MongoDB, ca_path: str):
    replica_set.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()
