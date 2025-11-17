from kubetester import (
    create_or_update_configmap,
    read_configmap,
)
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ShardedClusterTester
from kubetester.phase import Phase


class ShardedClusterCreationAndProjectSwitchTestHelper:
    def __init__(
        self,
        sharded_cluster: MongoDB,
        namespace: str,
        authentication_mechanism: str,
        expected_num_deployment_auth_mechanisms=2,
        active_auth_mechanism=True,
    ):
        self.sharded_cluster = sharded_cluster
        self.namespace = namespace
        self.authentication_mechanism = authentication_mechanism
        self.expected_num_deployment_auth_mechanisms = expected_num_deployment_auth_mechanisms
        self.active_auth_mechanism = active_auth_mechanism

    def test_create_sharded_cluster(self):
        self.sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)

    def test_sharded_cluster_connectivity(self, shard_count):
        ShardedClusterTester(self.sharded_cluster.name, shard_count).assert_connectivity()

    def test_ops_manager_state_with_expected_authentication(self, expected_users: int):
        tester = self.sharded_cluster.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled(self.authentication_mechanism, self.active_auth_mechanism)
        tester.assert_authentication_enabled(self.expected_num_deployment_auth_mechanisms)
        tester.assert_expected_users(expected_users)

        if self.authentication_mechanism == "MONGODB-X509":
            tester.assert_authoritative_set(True)

    def test_switch_sharded_cluster_project(self):
        original_tester = self.sharded_cluster.get_automation_config_tester()
        original_automation_agent_password = original_tester.get_automation_agent_password
        original_configmap = read_configmap(namespace=self.namespace, name="my-project")
        new_project_name = f"{self.namespace}-second"

        new_project_configmap = create_or_update_configmap(
            namespace=self.namespace,
            name=new_project_name,
            data={
                "baseUrl": original_configmap["baseUrl"],
                "projectName": new_project_name,
                "orgId": original_configmap["orgId"],
            },
        )
        self.sharded_cluster["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        self.sharded_cluster.update()
        self.sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)
        switched_tester = self.sharded_cluster.get_automation_config_tester()
        switched_automation_agent_password = switched_tester.get_automation_agent_password
        
        assert original_automation_agent_password == switched_automation_agent_password, (  
        "The automation agent password changed after switching the project."  
    )  


    def test_ops_manager_state_with_users(self, user_name: str, expected_roles: set, expected_users: int):
        tester = self.sharded_cluster.get_automation_config_tester()
        tester.assert_has_user(user_name)
        tester.assert_user_has_roles(user_name, expected_roles)
        tester.assert_expected_users(expected_users)
