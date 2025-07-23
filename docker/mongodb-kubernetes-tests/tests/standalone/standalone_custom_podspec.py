from kubetester.custom_podspec import assert_stateful_set_podspec
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def standalone(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("standalone-custom-podspec.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource.create()


@mark.e2e_standalone_custom_podspec
def test_replica_set_reaches_running_phase(standalone):
    standalone.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_standalone_custom_podspec
def test_stateful_set_spec_updated(standalone, namespace):
    appsv1 = KubernetesTester.clients("appsv1")
    sts = appsv1.read_namespaced_stateful_set(standalone.name, namespace)
    assert_stateful_set_podspec(sts.spec.template.spec, weight=50, topology_key="mykey", grace_period_seconds=10)

    containers = sts.spec.template.spec.containers
    container_names = [container.name for container in containers]

    if is_default_architecture_static():
        assert len(containers) == 4
        assert "mongodb-agent" in container_names
        assert "mongodb-enterprise-database" in container_names
        assert "mongodb-agent-operator-utilities" in container_names
        assert "standalone-sidecar" in container_names
    else:
        assert len(containers) == 2
        assert "standalone-sidecar" in container_names
        assert "mongodb-agent" in container_names

    labels = sts.spec.template.metadata.labels
    assert labels["label1"] == "value1"
