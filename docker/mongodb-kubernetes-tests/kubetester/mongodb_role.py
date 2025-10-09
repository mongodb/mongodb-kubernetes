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

    def get_name(self) -> str:
        return self["metadata"]["name"]

    def get_role_name(self):
        return self["spec"]["role"]

    def get_role(self):
        return self["spec"]
