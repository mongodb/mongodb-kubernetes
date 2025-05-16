import pytest
from kubernetes.client import V1ConfigMap
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase


@pytest.fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("standalone.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource.update()


@pytest.mark.e2e_standalone_recovery
class TestStandaloneRecoversBadOmConfiguration(KubernetesTester):
    """
    name: Standalone broken OM connection
    description: |
      Creates a standalone with a bad OM connection (ConfigMap is broken) and ensures it enters a failed state |
      Then the config map is fixed and the standalone is expected to reach good state eventually
    """

    def test_standalone_reaches_failed_state(self, mdb: MongoDB):
        config_map = V1ConfigMap(data={"baseUrl": "http://foo.bar"})
        self.clients("corev1").patch_namespaced_config_map("my-project", self.get_namespace(), config_map)

        mdb.assert_reaches_phase(Phase.Failed, timeout=20)

        mdb.load()
        assert "Failed to prepare Ops Manager connection" in mdb["status"]["message"]

    def test_recovery(self, mdb: MongoDB):
        config_map = V1ConfigMap(data={"baseUrl": KubernetesTester.get_om_base_url()})
        self.clients("corev1").patch_namespaced_config_map("my-project", KubernetesTester.get_namespace(), config_map)
        # We need to ignore errors here because the CM change can be faster than the check
        mdb.assert_reaches_phase(Phase.Running, ignore_errors=True)
