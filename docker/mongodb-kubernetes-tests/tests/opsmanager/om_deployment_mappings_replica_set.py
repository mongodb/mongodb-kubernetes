from typing import Optional

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from pytest import mark
from tests.common.ops_manager.deployment_mappings import DeploymentMappingsLifecycle, replica_set_cluster_id


@mark.e2e_om_deployment_mappings_replica_set
class TestReplicaSetDeploymentMappings(DeploymentMappingsLifecycle):
    def build(self, ops_manager: MongoDBOpsManager, namespace: str, mdb_version: str) -> MongoDB:
        resource = MongoDB.from_yaml(yaml_fixture("replica-set-for-om.yaml"), namespace=namespace).configure(
            ops_manager, "deploymentMappingsReplicaSet"
        )
        resource.set_version(mdb_version)
        return resource

    def deployment_name(self, mdb: MongoDB) -> str:
        return mdb.name

    def deployment_uuid(self, tester: OMTester, mdb: MongoDB) -> Optional[str]:
        return replica_set_cluster_id(tester, mdb.name)
