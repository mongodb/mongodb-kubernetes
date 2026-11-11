from pytest import mark
from tests.common.ops_manager.deployment_mappings import DeploymentMappingsLifecycle, ShardedClusterTopology


@mark.e2e_om_deployment_mappings_sharded_cluster
class TestShardedClusterDeploymentMappings(ShardedClusterTopology, DeploymentMappingsLifecycle):
    pass
