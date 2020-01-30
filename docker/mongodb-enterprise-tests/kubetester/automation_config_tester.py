from typing import Dict, Set, Tuple

X509_AGENT_SUBJECT = "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US"
SCRAM_AGENT_USER = "mms-automation-agent"


class AutomationConfigTester:
    """Tester for AutomationConfig. Should be initialized with the
    AutomationConfig we will test (`ac`), the expected amount of users, and if it should be
    set to `authoritative_set`, which means that the Automation Agent will force the existing
    users in MongoDB to be the ones defined in the Automation Config.
    """

    def __init__(
        self, ac: Dict, expected_users: int = 2, authoritative_set: bool = True
    ):
        self.automation_config = ac
        self.expected_users = expected_users
        self.authoritative_set = authoritative_set

    def assert_authentication_mechanism_enabled(
        self, mechanism: str, active_auth_mechanism: bool = True
    ) -> None:
        auth: dict = self.automation_config["auth"]
        assert mechanism in auth.get("deploymentAuthMechanisms", [])
        assert auth["authoritativeSet"] == self.authoritative_set
        if active_auth_mechanism:
            assert mechanism in auth.get("autoAuthMechanisms", [])
            assert auth["autoAuthMechanism"] == mechanism

        assert len(auth["usersWanted"]) == self.expected_users

    def assert_authentication_mechanism_disabled(self, mechanism: str) -> None:
        auth = self.automation_config["auth"]
        assert mechanism not in auth.get("deploymentAuthMechanisms", [])
        assert mechanism not in auth.get("autoAuthMechanisms", [])
        assert auth["autoAuthMechanism"] != mechanism

    def assert_authentication_enabled(
        self, expected_num_deployment_auth_mechanisms: int = 1
    ) -> None:
        assert not self.automation_config["auth"]["disabled"]
        assert len(self.automation_config["auth"]["usersWanted"]) == self.expected_users
        actual_num_deployment_auth_mechanisms = len(
            self.automation_config["auth"].get("deploymentAuthMechanisms", [])
        )
        assert (
            actual_num_deployment_auth_mechanisms
            == expected_num_deployment_auth_mechanisms
        )

    def assert_authentication_disabled(self, remaining_users: int = 0) -> None:
        assert self.automation_config["auth"]["disabled"]
        assert len(self.automation_config["auth"]["usersWanted"]) == remaining_users
        assert (
            len(self.automation_config["auth"].get("deploymentAuthMechanisms", [])) == 0
        )

    def assert_user_has_roles(self, username: str, roles: Set[Tuple[str, str]]) -> None:
        user = [
            user
            for user in self.automation_config["auth"]["usersWanted"]
            if user["user"] == username
        ][0]
        actual_roles = {(role["db"], role["role"]) for role in user["roles"]}
        assert actual_roles == roles

    def assert_has_user(self, username: str) -> None:
        assert username in {
            user["user"] for user in self.automation_config["auth"]["usersWanted"]
        }

    def assert_agent_user(self, agent_user: str) -> None:
        assert self.automation_config["auth"]["autoUser"] == agent_user

    def assert_replica_sets_size(self, expected_size: int):
        assert len(self.automation_config["replicaSets"]) == expected_size

    def assert_processes_size(self, expected_size: int):
        assert len(self.automation_config["processes"]) == expected_size

    def assert_sharding_size(self, expected_size: int):
        assert len(self.automation_config["sharding"]) == expected_size

    def assert_empty(self):
        self.assert_processes_size(0)
        self.assert_replica_sets_size(0)
        self.assert_sharding_size(0)

    def reached_version(self, version: int) -> bool:
        return self.automation_config["version"] == version
