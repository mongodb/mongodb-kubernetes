from __future__ import annotations

from typing import Optional

from kubeobject import CustomObject
from kubetester.mongodb_common import MongoDBCommon
from kubetester.phase import Phase


class MongoDBStandbyCluster(CustomObject, MongoDBCommon):
    """Wrapper for the MongoDBStandbyCluster custom resource."""

    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbstandbyclusters",
            "kind": "MongoDBStandbyCluster",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super().__init__(*args, **with_defaults)

    @classmethod
    def from_yaml(cls, yaml_file, name=None, namespace=None) -> MongoDBStandbyCluster:
        return super().from_yaml(yaml_file=yaml_file, name=name, namespace=namespace)

    def get_status_phase(self) -> Optional[Phase]:
        try:
            return Phase[self["status"]["phase"]]
        except (KeyError, AttributeError, TypeError):
            return None

    def assert_reaches_phase(self, phase: Phase, timeout=None):
        """Block until the resource reaches the given phase or raise on timeout."""
        self.wait_for(
            lambda s: s.get_status_phase() == phase,
            timeout,
            should_raise=True,
        )
