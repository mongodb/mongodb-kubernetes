from __future__ import annotations

from dataclasses import dataclass
from typing import List, Optional

from kubeobject import CustomObject
from kubetester import random_k8s_name
from kubetester.mongodb import MongoDB, MongoDBCommon, Phase, in_desired_state


class MongoDBUser(CustomObject, MongoDBCommon):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbusers",
            "kind": "MongoDBUser",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBUser, self).__init__(*args, **with_defaults)

    @property
    def password(self):
        return self._password

    def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False):
        return self.wait_for(
            lambda s: in_desired_state(
                current_state=self.get_status_phase(),
                desired_state=phase,
                current_generation=self.get_generation(),
                observed_generation=self.get_status_observed_generation(),
                current_message=self.get_status_message(),
                msg_regexp=msg_regexp,
                ignore_errors=ignore_errors,
            ),
            timeout,
            should_raise=True,
        )

    def get_user_name(self):
        return self["spec"]["username"]

    def get_secret_name(self) -> str:
        return self["spec"]["passwordSecretKeyRef"]["name"]

    def get_status_phase(self) -> Optional[Phase]:
        try:
            return Phase[self["status"]["phase"]]
        except KeyError:
            return None

    def get_status_message(self) -> Optional[str]:
        try:
            return self["status"]["msg"]
        except KeyError:
            return None

    def get_status_observed_generation(self) -> Optional[int]:
        try:
            return self["status"]["observedGeneration"]
        except KeyError:
            return None

    def add_role(self, role: Role) -> MongoDBUser:
        self["spec"]["roles"] = self["spec"].get("roles", [])
        self["spec"]["roles"].append({"db": role.db, "name": role.role})

    def add_roles(self, roles: List[Role]):
        for role in roles:
            self.add_role(role)


@dataclass(init=True)
class Role:
    db: str
    role: str


def generic_user(
    namespace: str,
    username: str,
    db: str = "admin",
    password: Optional[str] = None,
    mongodb_resource: Optional[MongoDB] = None,
) -> MongoDBUser:
    """Returns a generic User with a username and a pseudo-random k8s name."""
    user = MongoDBUser(name=random_k8s_name("user-"), namespace=namespace)
    user["spec"] = {
        "username": username,
        "db": db,
    }

    if mongodb_resource is not None:
        user["spec"]["mongodbResourceRef"] = {"name": mongodb_resource.name}

    user._password = password

    return user
