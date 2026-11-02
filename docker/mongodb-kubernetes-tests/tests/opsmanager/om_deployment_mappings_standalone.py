from typing import Optional

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from pytest import mark
from tests.common.ops_manager.deployment_mappings import DeploymentMappingsLifecycle, standalone_host_id


@mark.e2e_om_deployment_mappings_standalone
class TestStandaloneDeploymentMappings(DeploymentMappingsLifecycle):
    def build(self, ops_manager: MongoDBOpsManager, namespace: str, mdb_version: str) -> MongoDB:
        resource = MongoDB.from_yaml(yaml_fixture("standalone-for-om.yaml"), namespace=namespace).configure(
            ops_manager, "deploymentMappingsStandalone"
        )
        resource.set_version(mdb_version)
        return resource

    def deployment_name(self, mdb: MongoDB) -> str:
        # Standalones are mapped by process hostname, not by resource name.
        return f"{mdb.name}-0.{mdb.get_service()}.{mdb.namespace}.svc.{mdb.get_cluster_domain()}"

    def deployment_uuid(self, tester: OMTester, mdb: MongoDB) -> Optional[str]:
        return standalone_host_id(tester, self.deployment_name(mdb))
