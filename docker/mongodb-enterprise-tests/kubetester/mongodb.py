import time

from kubeobject import CustomObject
from .mongotester import (
    MongoTester,
    ReplicaSetTester,
    ShardedClusterTester,
    StandaloneTester,
)


class MongoDB(CustomObject):
    def __init__(
        self,
        name: str,
        namespace: str,
        plural: str = "mongodb",
        kind: str = "MongoDB",
        api_version: str = "mongodb.com/v1",
    ):
        super(self.__class__, self).__init__(name, namespace, plural, kind, api_version)

    def wait_for_phase(self, phase, timeout=240):
        """Waits until object reaches given state. The solution currently
        implemented is super simple and very similar to what we already have,
        but does the job well.
        """
        return self.wait_for(lambda s: s["status"].get("phase") == phase)

    def wait_for(self, fn, timeout=240):
        wait = 5
        while True:
            self.reload()
            try:
                if fn(self):
                    return True
            except Exception:
                pass

            if timeout > 0:
                timeout -= wait
                time.sleep(wait)
            else:
                break

    def reaches_phase(self, phase):
        return self.wait_for_phase(phase)

    def abandons_phase(self, phase):
        return self.wait_for(lambda s: s["status"].get("phase") != phase)

    @property
    def type(self) -> str:
        return self["spec"]["type"]

    def _is_tls(self) -> bool:
        """Checks if this object is TLS enabled."""
        is_tls = False
        try:
            is_tls = self["spec"]["security"]["tls"]["enabled"]
        except KeyError:
            pass

        return is_tls

    def _tester(self) -> MongoTester:
        """Returns a Tester instance for this type of deployment."""
        if self.type == "ReplicaSet":
            return ReplicaSetTester(
                self.name, self["status"]["members"], self._is_tls()
            )
        elif self.type == "ShardedCluster":
            return ShardedClusterTester(
                self.name, self["spec"]["mongosCount"], self._is_tls()
            )
        elif self.type == "Standalone":
            return StandaloneTester(self.name, self._is_tls())

    def assert_connectivity(self):
        return self._tester().assert_connectivity()

    class Types:
        REPLICA_SET = "ReplicaSet"
        SHARDED_CLUSTER = "ShardedCluster"
        STANDALONE = "Standalone"
