import pytest
from kubernetes.client import V1ConfigMap
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase


@pytest.fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("replica-set-single.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource.update()


@pytest.mark.e2e_replica_set_config_map
def test_create_replica_set(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_replica_set_config_map
def test_patch_config_map(namespace: str, mdb: MongoDB):
    config_map = V1ConfigMap(data={"orgId": "wrongId"})
    KubernetesTester.clients("corev1").patch_namespaced_config_map("my-project", namespace, config_map)
    print('Patched the ConfigMap - changed orgId to "wrongId"')
    mdb.assert_reaches_phase(Phase.Failed, timeout=20)
