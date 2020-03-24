import re
from enum import Enum
from typing import Optional, Dict, Tuple

import time
from kubeobject import CustomObject
from kubetester.kubetester import KubernetesTester, build_host_fqdn
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
    Updated = 5


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
        intermediate_events = (
            "haven't reached READY",
            "Some agents failed to register",
            # Sometimes Cloud-QA timeouts so we anticipate to this
            "Error sending GET request to",
        )
        return self.wait_for(
            lambda s: in_desired_state(
                self.get_status_phase(),
                phase,
                self.get_status_message(),
                msg_regexp=msg_regexp,
                ignore_errors=ignore_errors,
                intermediate_events=intermediate_events,
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

    def _tester(self) -> MongoTester:
        """Returns a Tester instance for this type of deployment."""
        if self.type == "ReplicaSet":
            return ReplicaSetTester(
                self.name, self["status"]["members"], self.is_tls_enabled()
            )
        elif self.type == "ShardedCluster":
            return ShardedClusterTester(
                self.name, self["spec"]["mongosCount"], self.is_tls_enabled()
            )
        elif self.type == "Standalone":
            return StandaloneTester(self.name, self.is_tls_enabled())

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
        if "project" in self["spec"]:
            del self["spec"]["project"]

        self["spec"]["opsManager"] = {"configMapRef": {}}

        self["spec"]["opsManager"]["configMapRef"][
            "name"
        ] = om.get_or_create_mongodb_connection_config_map(self.name, project_name)
        self["spec"]["credentials"] = om.api_key_secret()
        return self

    def build_list_of_hosts(self):
        """ Returns the list of full_fqdn:27017 for every member of the mongodb resource """
        return [
            build_host_fqdn(
                f"{self.name}-{idx}",
                self.namespace,
                self.get_service(),
                self.get_cluster_domain(),
                27017,
            )
            for idx in range(self.get_members())
        ]

    def mongo_uri(
        self, user_name: Optional[str] = None, password: Optional[str] = None
    ) -> str:
        """ Returns the mongo uri for the MongoDB resource. The logic matches the one in 'types.go' """
        proto = "mongodb://"
        auth = ""
        params = {"connectTimeoutMS": "20000", "serverSelectionTimeoutMS": "20000"}
        if "SCRAM" in self.get_authentication_modes():
            auth = f"{user_name}:{password}@"
            params["authSource"] = "admin"
            # TODO check the version for correct auth mechanism
            params["authMechanism"] = "SCRAM-SHA-1"

        hosts = ",".join(self.build_list_of_hosts())

        if self.get_resource_type() == "ReplicaSet":
            params["replicaSet"] = self.name

        if self.is_tls_enabled():
            params["ssl"] = "true"

        query_params = [
            "{}={}".format(key, params[key]) for key in sorted(params.keys())
        ]
        joined_params = "&".join(query_params)
        return proto + auth + hosts + "/?" + joined_params

    def get_members(self) -> int:
        return self["spec"]["members"]

    def get_service(self) -> str:
        try:
            return self["spec"]["service"]
        except KeyError:
            return "{}-svc".format(self.name)

    def get_cluster_domain(self) -> Optional[str]:
        try:
            return self["spec"]["clusterDomain"]
        except KeyError:
            return "cluster.local"

    def get_resource_type(self) -> str:
        return self["spec"]["type"]

    def is_tls_enabled(self):
        """Checks if this object is TLS enabled."""
        try:
            return self["spec"]["security"]["tls"]["enabled"]
        except KeyError:
            return False

    def get_authentication(self) -> Optional[Dict]:
        try:
            return self["spec"]["security"]["authentication"]
        except KeyError:
            return {}

    def get_authentication_modes(self) -> Optional[Dict]:
        try:
            return self.get_authentication()["modes"]
        except KeyError:
            return {}

    def get_status_phase(self) -> Optional[Phase]:
        try:
            return Phase[self["status"]["phase"]]
        except KeyError:
            return None

    def get_status_message(self) -> Optional[str]:
        try:
            return self["status"]["message"]
        except KeyError:
            return None

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


def get_pods(podname, qty):
    return [podname.format(i) for i in range(qty)]


def in_desired_state(
    current_state: Phase,
    desired_state: Phase,
    current_message: str,
    msg_regexp: Optional[str] = None,
    ignore_errors=False,
    intermediate_events: Tuple = (),
) -> bool:
    """ Returns true if the current_state is equal to desired state, fails fast if got into Failed error.
     Optionally checks if the message matches the specified regexp expression"""
    if current_state is None:
        return False

    if (
        current_state == Phase.Failed
        and not desired_state == Phase.Failed
        and not ignore_errors
    ):
        found = False
        for event in intermediate_events:
            if event in current_message:
                found = True

        if not found:
            raise AssertionError(
                f'Got into Failed phase while waiting for Running! ("{current_message}")'
            )

    is_in_desired_state = current_state == desired_state
    if msg_regexp is not None:
        regexp = re.compile(msg_regexp)
        is_in_desired_state = (
            is_in_desired_state
            and current_message is not None
            and regexp.match(current_message)
        )

    return is_in_desired_state
