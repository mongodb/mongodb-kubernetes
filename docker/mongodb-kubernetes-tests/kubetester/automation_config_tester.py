from typing import Dict, List, Optional, Set, Tuple

from kubetester.kubetester import KubernetesTester

X509_AGENT_SUBJECT = "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US"
SCRAM_AGENT_USER = "mms-automation-agent"


class AutomationConfigTester:
    """Tester for AutomationConfig. Should be initialized with the
    AutomationConfig we will test (`ac`), the expected amount of users, and if it should be
    set to `authoritative_set`, which means that the Automation Agent will force the existing
    users in MongoDB to be the ones defined in the Automation Config.
    """

    def __init__(self, ac: Optional[Dict] = None):
        if ac is None:
            ac = KubernetesTester.get_automation_config()
        self.automation_config = ac

    def get_replica_set_processes(self, rs_name: str) -> List[Dict]:
        """Returns all processes for the specified replica set"""
        replica_set = ([rs for rs in self.automation_config["replicaSets"] if rs["_id"] == rs_name])[0]
        rs_processes_name = [member["host"] for member in replica_set["members"]]
        return [process for process in self.automation_config["processes"] if process["name"] in rs_processes_name]

    def get_replica_set_members(self, rs_name: str) -> List[Dict]:
        replica_set = ([rs for rs in self.automation_config["replicaSets"] if rs["_id"] == rs_name])[0]
        return sorted(replica_set["members"], key=lambda member: member["_id"])

    def get_mongos_processes(self):
        """ " Returns all mongos processes in deployment. We don't need to filter by sharded cluster name as
        we have only a single resource per deployment"""
        return [process for process in self.automation_config["processes"] if process["processType"] == "mongos"]

    def get_all_processes(self):
        return self.automation_config["processes"]

    def get_automation_agent_password(self):
        return self.automation_config["auth"]["autoPwd"]
    
    def assert_expected_users(self, expected_users: int):
        automation_config_users = 0

        for user in self.automation_config["auth"]["usersWanted"]:
            if user["user"] != "mms-backup-agent" and user["user"] != "mms-monitoring-agent":
                automation_config_users += 1

        assert automation_config_users == expected_users

    def assert_authoritative_set(self, authoritative_set: bool):
        assert self.automation_config["auth"]["authoritativeSet"] == authoritative_set

    def assert_authentication_mechanism_enabled(self, mechanism: str, active_auth_mechanism: bool = True) -> None:
        auth: dict = self.automation_config["auth"]
        assert mechanism in auth.get("deploymentAuthMechanisms", [])
        if active_auth_mechanism:
            assert mechanism in auth.get("autoAuthMechanisms", [])
            assert auth["autoAuthMechanism"] == mechanism

    def assert_authentication_mechanism_disabled(self, mechanism: str, check_auth_mechanism: bool = True) -> None:
        auth = self.automation_config["auth"]
        assert mechanism not in auth.get("deploymentAuthMechanisms", [])
        assert mechanism not in auth.get("autoAuthMechanisms", [])
        if check_auth_mechanism:
            assert auth["autoAuthMechanism"] != mechanism

    def assert_authentication_enabled(self, expected_num_deployment_auth_mechanisms: int = 1) -> None:
        assert not self.automation_config["auth"]["disabled"]

        actual_num_deployment_auth_mechanisms = len(self.automation_config["auth"].get("deploymentAuthMechanisms", []))
        assert actual_num_deployment_auth_mechanisms == expected_num_deployment_auth_mechanisms

    def assert_internal_cluster_authentication_enabled(self):
        for process in self.automation_config["processes"]:
            assert process["args2_6"]["security"]["clusterAuthMode"] == "x509"

    def assert_authentication_disabled(self, remaining_users: int = 0) -> None:
        assert self.automation_config["auth"]["disabled"]
        self.assert_expected_users(expected_users=remaining_users)
        assert len(self.automation_config["auth"].get("deploymentAuthMechanisms", [])) == 0

    def assert_user_has_roles(self, username: str, roles: Set[Tuple[str, str]]) -> None:
        user = [user for user in self.automation_config["auth"]["usersWanted"] if user["user"] == username][0]
        actual_roles = {(role["db"], role["role"]) for role in user["roles"]}
        assert actual_roles == roles

    def assert_has_user(self, username: str) -> None:
        assert username in {user["user"] for user in self.automation_config["auth"]["usersWanted"]}

    def assert_agent_user(self, agent_user: str) -> None:
        assert self.automation_config["auth"]["autoUser"] == agent_user

    def assert_replica_sets_size(self, expected_size: int):
        assert len(self.automation_config["replicaSets"]) == expected_size

    def assert_processes_size(self, expected_size: int):
        assert len(self.automation_config["processes"]) == expected_size

    def assert_sharding_size(self, expected_size: int):
        assert len(self.automation_config["sharding"]) == expected_size

    def assert_oidc_providers_size(self, expected_size: int):
        assert len(self.automation_config["oidcProviderConfigs"]) == expected_size

    def assert_oidc_configuration(self, oidc_config: Optional[Dict] = None):
        actual_configs = self.automation_config["oidcProviderConfigs"]
        assert len(actual_configs) == len(
            oidc_config
        ), f"Expected {len(oidc_config)} OIDC configs, but got {len(actual_configs)}"

        for expected, actual in zip(oidc_config, actual_configs):
            assert expected == actual, f"Expected OIDC config: {expected}, but got: {actual}"

    def assert_empty(self):
        self.assert_processes_size(0)
        self.assert_replica_sets_size(0)
        self.assert_sharding_size(0)

    def assert_mdb_option(self, process: Dict, value, *keys):
        current = process["args2_6"]
        for k in keys[:-1]:
            current = current[k]
        assert current[keys[-1]] == value

    def get_role_at_index(self, index: int) -> Dict:
        roles = self.automation_config["roles"]
        assert roles is not None
        assert len(roles) > index
        return roles[index]

    def assert_has_expected_number_of_roles(self, expected_roles: int):
        roles = self.automation_config["roles"]
        assert len(roles) == expected_roles

    def assert_expected_role(self, role_index: int, expected_value: Dict):
        role = self.automation_config["roles"][role_index]
        assert role == expected_value

    def assert_tls_client_certificate_mode(self, mode: str):
        assert self.automation_config["tls"]["clientCertificateMode"] == mode

    def reached_version(self, version: int) -> bool:
        return self.automation_config["version"] == version

    def get_agent_version(self) -> str:
        try:
            return self.automation_config["agentVersion"]["name"]
        except KeyError:
            # the agent version can be empty if the /automationConfig/upgrade endpoint hasn't been called yet
            return ""
