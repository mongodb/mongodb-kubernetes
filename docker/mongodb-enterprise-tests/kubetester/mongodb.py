import time

from typing import List

from kubeobject import CustomObject
from .mongotester import (
    MongoTester,
    ReplicaSetTester,
    ShardedClusterTester,
    StandaloneTester,
)

from kubernetes import client
from kubernetes.client.rest import ApiException


class MongoDBCommon:
    def wait_for(self, fn, timeout=None, should_raise=False):
        if timeout is None:
            timeout = 240
        initial_timeout = timeout

        wait = 3
        while timeout > 0:
            self.reload()
            try:
                if fn(self):
                    return True
            except Exception:
                pass
            timeout -= wait
            time.sleep(wait)

        if should_raise:
            raise Exception(
                "Timeout ({}) reached while waiting for {}".format(
                    initial_timeout, self
                )
            )


class MongoDB(CustomObject, MongoDBCommon):
    def __init__(self, *args, **kwargs):
        with_defaults = {"plural": "mongodb", "kind": "MongoDB", "group": "mongodb.com", "version": "v1"}
        with_defaults.update(kwargs)
        super(MongoDB, self).__init__(*args, **with_defaults)

    def assert_reaches_phase(self, phase, timeout=None):
        return self.wait_for(
            lambda s: s["status"].get("phase") == phase, timeout, should_raise=True
        )

    def assert_abandons_phase(self, phase, timeout=None):
        return self.wait_for(
            lambda s: s["status"].get("phase") != phase, timeout, should_raise=True
        )

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

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDB| status: {}| message: {}".format(
            self["status"].get("phase", ""), self["status"].get("message", "")
        )

    class Types:
        REPLICA_SET = "ReplicaSet"
        SHARDED_CLUSTER = "ShardedCluster"
        STANDALONE = "Standalone"


class MongoDBOpsManager(CustomObject, MongoDBCommon):
    def __init__(self, *args, **kwargs):
        with_defaults = {"plural": "opsmanagers", "kind": "MongoDBOpsManager", "group": "mongodb.com", "version": "v1"}
        with_defaults.update(kwargs)
        super(MongoDBOpsManager, self).__init__(*args, **with_defaults)

    def assert_reaches_phase(self, phase, timeout=None):
        return self.wait_for(
            lambda s: s["status"]["opsManager"]["phase"] == phase,
            timeout,
            should_raise=True,
        )

    def assert_abandons_phase(self, phase, timeout=None):
        return self.wait_for(
            lambda s: s["status"]["opsManager"]["phase"] != phase,
            timeout,
            should_raise=True,
        )

    def assert_reaches(self, fn, timeout=None):
        return self.wait_for(fn, timeout=timeout, should_raise=True)

    def services(self) -> List[client.V1Service]:
        """Returns a two element list with internal and external Services.

        Any of them might be None if the Service is not found.
        """
        services = []
        service_names = (self.name + "-svc", self.name + "-svc-ext")

        for name in service_names:
            try:
                svc = client.CoreV1Api().read_namespaced_service(name, self.namespace)
                services.append(svc)
            except ApiException:
                services.append(None)

        return services[0], services[1]

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDBOpsManager| status: {}| message: {}".format(
            self["status"]["opsManager"].get("phase", ""),
            self["status"]["opsManager"].get("message", ""),
        )
