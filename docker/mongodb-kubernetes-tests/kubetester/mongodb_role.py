from typing import Optional

from kubeobject import CustomObject
from kubetester.mongodb import MongoDBCommon, Phase, in_desired_state

ClusterMongoDBRoleKind = "ClusterMongoDBRole"


class ClusterMongoDBRole(CustomObject, MongoDBCommon):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "clustermongodbroles",
            "kind": "ClusterMongoDBRole",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(ClusterMongoDBRole, self).__init__(*args, **with_defaults)

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

    def get_name(self) -> str:
        return self["metadata"]["name"]

    def get_role_name(self):
        return self["spec"]["role"]

    def get_role(self):
        return self["spec"]

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
