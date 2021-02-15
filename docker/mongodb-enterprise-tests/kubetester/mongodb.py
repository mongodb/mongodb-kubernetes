from __future__ import annotations
import re
from enum import Enum
import urllib.parse
from typing import Optional, Dict, Tuple, List

import time
from kubeobject import CustomObject
from kubernetes import client
from kubernetes.client import V1ConfigMap

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

    def get_generation(self) -> int:
        return self.backing_obj["metadata"]["generation"]


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
            # "Get https://cloud-qa.mongodb.com/api/public/v1.0/groups/5f186b406c835e37e6160aef/automationConfig:
            # read tcp 10.244.0.6:33672->75.2.105.99:443: read: connection reset by peer"
            "read: connection reset by peer",
        )
        return self.wait_for(
            lambda s: in_desired_state(
                current_state=self.get_status_phase(),
                desired_state=phase,
                current_generation=self.get_generation(),
                observed_generation=self.get_status_observed_generation(),
                current_message=self.get_status_message(),
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

    def assert_status_resource_not_ready(
        self, name: str, kind: str = "StatefulSet", msg_regexp=None, idx=0
    ):
        """Checks the element in 'resources_not_ready' field by index 'idx' """
        assert self.get_status_resources_not_ready()[idx]["kind"] == kind
        assert self.get_status_resources_not_ready()[idx]["name"] == name
        assert (
            re.search(msg_regexp, self.get_status_resources_not_ready()[idx]["message"])
            is not None
        )

    @property
    def type(self) -> str:
        return self["spec"]["type"]

    def tester(
        self,
        ca_path: Optional[str] = None,
        srv: bool = False,
        use_ssl: Optional[bool] = None,
    ) -> MongoTester:
        """Returns a Tester instance for this type of deployment."""
        if self.type == "ReplicaSet":
            return ReplicaSetTester(
                mdb_resource_name=self.name,
                replicas_count=self["status"]["members"],
                ssl=self.is_tls_enabled() if use_ssl is None else use_ssl,
                srv=srv,
                ca_path=ca_path,
                namespace=self.namespace,
            )
        elif self.type == "ShardedCluster":
            return ShardedClusterTester(
                mdb_resource_name=self.name,
                mongos_count=self["spec"]["mongosCount"],
                ssl=self.is_tls_enabled() if use_ssl is None else use_ssl,
                srv=srv,
                ca_path=ca_path,
                namespace=self.namespace,
            )
        elif self.type == "Standalone":
            return StandaloneTester(
                mdb_resource_name=self.name,
                ssl=self.is_tls_enabled() if use_ssl is None else use_ssl,
                srv=srv,
                ca_path=ca_path,
                namespace=self.namespace,
            )

    def assert_connectivity(self, ca_path: Optional[str] = None):
        return self.tester(ca_path).assert_connectivity()

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDB ({})| status: {}| message: {}".format(
            self.name, self.get_status_phase(), self.get_status_message()
        )

    def configure(self, om, project_name: str):
        if "project" in self["spec"]:
            del self["spec"]["project"]

        self["spec"]["opsManager"] = {"configMapRef": {}}

        self["spec"]["opsManager"]["configMapRef"][
            "name"
        ] = om.get_or_create_mongodb_connection_config_map(
            self.name, project_name, self.namespace
        )
        # Note that if the MongoDB object is created in a different namespace than the Operator
        # then the secret needs to be copied there manually
        self["spec"]["credentials"] = om.api_key_secret(self.namespace)
        return self

    def configure_backup(self, mode: str = "enabled") -> MongoDB:
        if "backup" not in self["spec"]:
            self["spec"]["backup"] = {}

        self["spec"]["backup"]["mode"] = mode
        return self

    def configure_custom_tls(
        self, issuer_ca_configmap_name: str, tls_cert_secret_name: str
    ):
        if "security" not in self["spec"]:
            self["spec"]["security"] = {}
        if "tls" not in self["spec"]["security"]:
            self["spec"]["security"]["tls"] = {}

        self["spec"]["security"]["tls"]["enabled"] = True
        self["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap_name
        self["spec"]["security"]["tls"]["secretRef"] = {"name": tls_cert_secret_name}

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

    def read_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.name, self.namespace
        )

    def read_configmap(self) -> Dict[str, str]:
        return KubernetesTester.read_configmap(self.namespace, self.config_map_name)

    def mongo_uri(
        self, user_name: Optional[str] = None, password: Optional[str] = None
    ) -> str:
        """ Returns the mongo uri for the MongoDB resource. The logic matches the one in 'types.go' """
        proto = "mongodb://"
        auth = ""
        params = {"connectTimeoutMS": "20000", "serverSelectionTimeoutMS": "20000"}
        if "SCRAM" in self.get_authentication_modes():
            auth = "{}:{}@".format(
                urllib.parse.quote(user_name, safe=""),
                urllib.parse.quote(password, safe=""),
            )
            params["authSource"] = "admin"
            if self.get_version().startswith("3.6"):
                params["authMechanism"] = "SCRAM-SHA-1"
            else:
                params["authMechanism"] = "SCRAM-SHA-256"

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

    def get_version(self) -> str:
        return self["spec"]["version"]

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

    def set_version(self, version: str):
        self["spec"]["version"] = version

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

    def get_status_observed_generation(self) -> Optional[int]:
        try:
            return self["status"]["observedGeneration"]
        except KeyError:
            return None

    def get_status_members(self) -> Optional[str]:
        try:
            return self["status"]["members"]
        except KeyError:
            return None

    def get_status_resources_not_ready(self) -> Optional[List[Dict]]:
        try:
            return self["status"]["resourcesNotReady"]
        except KeyError:
            return None

    def get_om_tester(self) -> OMTester:
        """ Returns the OMTester instance based on MongoDB connectivity parameters """
        config_map = self.read_configmap()
        secret = KubernetesTester.read_secret(
            self.namespace, self["spec"]["credentials"]
        )
        return OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))

    def get_automation_config_tester(self, **kwargs):
        """ This is just a shortcut for getting automation config tester for replica set"""
        return self.get_om_tester().get_automation_config_tester(**kwargs)

    @property
    def config_map_name(self) -> str:
        if "opsManager" in self["spec"]:
            return self["spec"]["opsManager"]["configMapRef"]["name"]
        return self["spec"]["project"]

    def config_srv_statefulset_name(self) -> str:
        return self.name + "-config"

    def shards_statefulsets_names(self) -> List[str]:
        return [
            "{}-{}".format(self.name, i) for i in range(1, self["spec"]["shardCount"])
        ]

    class Types:
        REPLICA_SET = "ReplicaSet"
        SHARDED_CLUSTER = "ShardedCluster"
        STANDALONE = "Standalone"


def get_pods(podname, qty):
    return [podname.format(i) for i in range(qty)]


def in_desired_state(
    current_state: Phase,
    desired_state: Phase,
    current_generation: int,
    observed_generation: int,
    current_message: str,
    msg_regexp: Optional[str] = None,
    ignore_errors=False,
    intermediate_events: Tuple = (),
) -> bool:

    """Returns true if the current_state is equal to desired state, fails fast if got into Failed error.
    Optionally checks if the message matches the specified regexp expression"""
    if current_state is None:
        return False

    # TODO remove the first condition after 1.8.1 is released (this is needed to make operator_upgrade tests work as 1.8.0 doesn't have 'observedGeneration')
    if observed_generation is not None and current_generation != observed_generation:
        # We shouldn't check the status further if the Operator hasn't started working on the new spec yet
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
