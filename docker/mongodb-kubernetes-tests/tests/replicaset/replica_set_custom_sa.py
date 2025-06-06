from kubetester import create_service_account, delete_service_account
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def create_custom_sa(namespace: str) -> str:
    return create_service_account(namespace=namespace, name="test-sa")


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str, create_custom_sa: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)

    resource["spec"]["podSpec"] = {"podTemplate": {"spec": {"serviceAccountName": "test-sa"}}}
    resource["spec"]["statefulSet"] = {"spec": {"serviceName": "rs-svc"}}
    resource.set_version(custom_mdb_version)
    yield resource.create()
    # teardown, delete the custom service-account
    delete_service_account(namespace=namespace, name="test-sa")


@mark.e2e_replica_set_custom_sa
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replica_set_custom_sa
def test_stateful_set_spec_service_account(replica_set: MongoDB, namespace: str):
    appsv1 = KubernetesTester.clients("appsv1")
    sts = appsv1.read_namespaced_stateful_set(replica_set.name, namespace)

    assert sts.spec.template.spec.service_account_name == "test-sa"


@mark.e2e_replica_set_custom_sa
def test_service_is_created(namespace: str):
    corev1 = KubernetesTester.clients("corev1")
    svc = corev1.read_namespaced_service("rs-svc", namespace)
    assert svc
