import re
from enum import Enum
from typing import List, Optional, Dict

import time
from kubeobject import CustomObject
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester.kubetester import KubernetesTester
from kubetester.omtester import OMTester, OMContext

from .mongotester import (
    MongoTester,
    ReplicaSetTester,
    ShardedClusterTester,
    StandaloneTester,
)


class Phase(Enum):
    Running = 1
    Pending = 2
    Failed = 3
    Reconciling = 4


class MongoDBCommon:
    def wait_for(self, fn, timeout=None, should_raise=False):
        if timeout is None:
            timeout = 240
        initial_timeout = timeout

        wait = 3
        while timeout > 0:
            self.reload()
            if fn(self):
                return True
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
        with_defaults = {
            "plural": "mongodb",
            "kind": "MongoDB",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDB, self).__init__(*args, **with_defaults)

    def assert_reaches_phase(
        self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False
    ):
        return self.wait_for(
            lambda s: s.in_desired_state(
                phase, msg_regexp=msg_regexp, ignore_errors=ignore_errors
            ),
            timeout,
            should_raise=True,
        )

    def assert_abandons_phase(self, phase: Phase, timeout=None):
        return self.wait_for(
            lambda s: s.get_status_phase() != phase, timeout, should_raise=True
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
        return "MongoDB ({})| status: {}| message: {}".format(
            self.name,
            self["status"].get("phase", ""),
            self["status"].get("message", ""),
        )

    def configure(self, om, project_name: str):
        self["spec"]["opsManager"]["configMapRef"][
            "name"
        ] = om.get_or_create_mongodb_connection_config_map(self.name, project_name)
        self["spec"]["credentials"] = om.api_key_secret()
        return self

    def in_desired_state(
        self, state: Phase, msg_regexp: Optional[str] = None, ignore_errors=False
    ) -> bool:
        """ Returns true if the MongoDB is in desired state, fails fast if got into Failed error.
         Optionally checks if the message matches the specified regexp expression"""
        if "status" not in self:
            return False

        intermediate_events = (
            "haven't reached READY",
            "Some agents failed to register",
            # Sometimes Cloud-QA timeouts so we anticipate to this
            "Error sending GET request to",
        )

        if self.get_status_phase() == Phase.Failed and not ignore_errors:
            found = False
            for event in intermediate_events:
                if event in self.get_status_message():
                    found = True

            if not found:
                raise AssertionError(
                    'Got into Failed phase while waiting for Running! ("{}")'.format(
                        self["status"]["message"]
                    )
                )
        is_in_desired_state = self.get_status_phase() == state
        if msg_regexp is not None:
            regexp = re.compile(msg_regexp)
            is_in_desired_state = (
                is_in_desired_state
                and self.get_status_message() is not None
                and regexp.match(self.get_status_message())
            )

        return is_in_desired_state

    def get_status(self) -> Optional:
        return self["status"]

    def get_status_phase(self) -> Optional[Phase]:
        return Phase[self.get_status()["phase"]]

    def get_status_message(self) -> Optional[str]:
        if "message" not in self.get_status():
            return None
        return self.get_status()["message"]

    def get_om_tester(self):
        """ Returns the OMTester instance based on MongoDB connectivity parameters """
        config_map = KubernetesTester.read_configmap(
            self.namespace, self.config_map_name
        )
        secret = KubernetesTester.read_secret(
            self.namespace, self["spec"]["credentials"]
        )
        return OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))

    def get_automation_config_tester(self):
        """ This is just a shortcut for getting automation config tester for replica set"""
        return self.get_om_tester().get_automation_config_tester()

    @property
    def config_map_name(self) -> str:
        if "opsManager" in self["spec"]:
            return self["spec"]["opsManager"]["configMapRef"]["name"]
        return self["spec"]["project"]

    class Types:
        REPLICA_SET = "ReplicaSet"
        SHARDED_CLUSTER = "ShardedCluster"
        STANDALONE = "Standalone"


class MongoDBOpsManager(CustomObject, MongoDBCommon):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "opsmanagers",
            "kind": "MongoDBOpsManager",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBOpsManager, self).__init__(*args, **with_defaults)

    def assert_reaches_phase(
        self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False
    ):
        self.wait_for(
            lambda s: s.in_desired_state(
                phase, msg_regexp=msg_regexp, ignore_errors=ignore_errors
            ),
            timeout,
            should_raise=True,
        )
        if phase == Phase.Running:
            assert Phase[self.get_appdb_status()["phase"]] == Phase.Running

    def assert_abandons_phase(self, phase: Phase, timeout=None):
        return self.wait_for(
            lambda s: s.get_om_status_phase() != phase, timeout, should_raise=True,
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

    def get_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.name, self.namespace
        )

    def get_or_create_mongodb_connection_config_map(
        self, mongodb_name: str, project_name: str
    ) -> str:
        config_map_name = f"{mongodb_name}-config"
        try:
            KubernetesTester.create_configmap(
                self.namespace,
                config_map_name,
                {"baseUrl": self.get_om_status_url(), "projectName": project_name},
            )
        except ApiException as e:
            if e.status != 409:
                raise
        return config_map_name

    def in_desired_state(
        self, state: Phase, msg_regexp: Optional[str] = None, ignore_errors=False
    ) -> bool:
        """ Returns true if the resource in desired state, fails fast if got into Failed error.
         This allows to fail fast in case of cascade failures. If message regexp is specified than
         the CR message must match the expected one """
        if self.get_status() is None:
            return False
        phase = self.get_om_status_phase()

        if phase == Phase.Failed and not ignore_errors:
            msg = self.get_om_status()["message"]
            raise AssertionError(
                'Got into Failed phase while waiting for Running! ("{}")'.format(msg)
            )

        is_om_in_desired_state = phase == state
        if msg_regexp is not None:
            regexp = re.compile(msg_regexp)
            is_om_in_desired_state = is_om_in_desired_state and regexp.match(
                self.get_om_status()["message"]
            )

        return is_om_in_desired_state

    def get_om_tester(self) -> OMTester:
        """ Returns the instance of OMTester helping to check the state of Ops Manager deployed in Kubernetes. """
        api_key_secret = KubernetesTester.read_secret(
            KubernetesTester.get_namespace(), self.api_key_secret()
        )
        om_context = OMContext(
            self.get_om_status_url(),
            api_key_secret["user"],
            api_key_secret["publicApiKey"],
        )
        return OMTester(om_context)

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDBOpsManager| status: {}| message: {}".format(
            self["status"]["opsManager"].get("phase", ""),
            self["status"]["opsManager"].get("message", ""),
        )

    def get_status(self) -> Optional:
        if "status" not in self:
            return None
        return self["status"]

    def get_om_status(self) -> Optional:
        if self.get_status() is None:
            return None
        return self.get_status()["opsManager"]

    def get_om_status_phase(self) -> Optional[Phase]:
        if self.get_om_status() is None:
            return None
        return Phase[self.get_om_status()["phase"]]

    def get_appdb_status(self) -> Optional[Dict]:
        if self.get_status() is None:
            return None
        return self.get_status()["applicationDatabase"]

    def get_om_status_last_transition(self) -> Optional[int]:
        if self.get_om_status() is None:
            return None
        return self.get_om_status()["lastTransition"]

    def get_om_status_url(self) -> Optional[str]:
        if self.get_om_status() is None or "url" not in self.get_om_status():
            return None
        return self.get_om_status()["url"]

    def api_key_secret(self) -> str:
        return self.name + "-admin-key"
