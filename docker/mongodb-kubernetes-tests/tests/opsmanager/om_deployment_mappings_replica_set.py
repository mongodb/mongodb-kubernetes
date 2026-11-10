from pytest import mark
from tests.common.ops_manager.deployment_mappings import DeploymentMappingsLifecycle, ReplicaSetTopology


@mark.e2e_om_deployment_mappings_replica_set
class TestReplicaSetDeploymentMappings(ReplicaSetTopology, DeploymentMappingsLifecycle):
    pass
