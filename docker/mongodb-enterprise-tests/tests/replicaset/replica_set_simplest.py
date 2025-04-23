import pytest
from kubernetes.client import V1ConfigMap
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase


@pytest.fixture(scope="function")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("replica-set-single.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = True
    return resource


@pytest.mark.e2e_replica_set_config_map
def test_create_replica_set(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running)
