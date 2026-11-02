from typing import Optional

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from pytest import mark
from tests.common.ops_manager.deployment_mappings import DeploymentMappingsLifecycle, sharded_cluster_id


@mark.e2e_om_deployment_mappings_sharded_cluster
class TestShardedClusterDeploymentMappings(DeploymentMappingsLifecycle):
    running_timeout = 1200

    def build(self, ops_manager: MongoDBOpsManager, namespace: str, mdb_version: str) -> MongoDB:
        resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-for-om.yaml"), namespace=namespace).configure(
            ops_manager, "deploymentMappingsShardedCluster"
        )
        resource.set_version(mdb_version)
        resource["spec"]["shardCount"] = 1
        resource["spec"]["mongodsPerShardCount"] = 1
        return resource

    def deployment_name(self, mdb: MongoDB) -> str:
        return mdb.name

    def deployment_uuid(self, tester: OMTester, mdb: MongoDB) -> Optional[str]:
        return sharded_cluster_id(tester, mdb.name)
