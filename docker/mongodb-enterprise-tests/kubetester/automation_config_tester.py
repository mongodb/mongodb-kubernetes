from typing import Dict, Set, Tuple


class AutomationConfigTester:
    def __init__(self, ac: Dict, expected_users: int = 2, authoritative_set: bool = True):
        self.automation_config = ac
        self.expected_users = expected_users
        self.authoritative_set = authoritative_set

    def assert_authentication_mechanism_enabled(self, mechanism: str, active_auth_mechanism: bool = True) -> None:
        auth = self.automation_config["auth"]
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

    def assert_authentication_enabled(self, expected_num_deployment_auth_mechanisms: int = 1) -> None:
        assert not self.automation_config["auth"]["disabled"]
        assert len(self.automation_config["auth"]["usersWanted"]) == self.expected_users
        actual_num_deployment_auth_mechanisms = len(self.automation_config["auth"].get("deploymentAuthMechanisms", []))
        assert actual_num_deployment_auth_mechanisms == expected_num_deployment_auth_mechanisms

    def assert_authentication_disabled(self, remaining_users: int = 0) -> None:
        assert self.automation_config["auth"]["disabled"]
        assert len(self.automation_config["auth"]["usersWanted"]) == remaining_users
        assert len(self.automation_config["auth"].get("deploymentAuthMechanisms", [])) == 0

    def assert_user_has_roles(self, username: str, roles: Set[Tuple[str, str]]) -> None:
        user = [user for user in self.automation_config["auth"]["usersWanted"] if user["user"] == username][0]
        actual_roles = {(role["db"], role["role"]) for role in user["roles"]}
        assert actual_roles == roles

    def assert_has_user(self, username: str) -> None:
        assert username in {user["user"] for user in self.automation_config["auth"]["usersWanted"]}
