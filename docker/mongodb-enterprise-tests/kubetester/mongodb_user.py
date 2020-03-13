from typing import Optional
import re


from kubeobject import CustomObject
from kubetester.mongodb import MongoDBCommon, Phase, in_desired_state


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

    def assert_reaches_phase(
        self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False
    ):
        return self.wait_for(
            lambda s: in_desired_state(
                self.get_status_phase(),
                phase,
                self.get_status_message(),
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
