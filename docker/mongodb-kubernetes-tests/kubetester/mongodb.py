from __future__ import annotations

import os
import re
import time
import urllib.parse
from typing import Dict, List, Optional

import kubernetes
import semver
from kubeobject import CustomObject
from kubernetes import client
from kubetester import create_or_update_configmap
from kubetester.kubetester import (
    KubernetesTester,
    build_host_fqdn,
    ensure_ent_version,
    ensure_nested_objects,
    is_default_architecture_static,
)
from kubetester.omtester import OMContext, OMTester
from opentelemetry import trace
from tests import test_logger

from .mongodb_common import MongoDBCommon
from .mongodb_utils_state import in_desired_state
from .mongotester import (
    MongoTester,
    ReplicaSetTester,
    ShardedClusterTester,
    StandaloneTester,
)
from .opsmanager import MongoDBOpsManager
from .phase import Phase

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


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

    @classmethod
    def from_yaml(cls, yaml_file, name=None, namespace=None, with_mdb_version_from_env=True) -> MongoDB:
        resource = super().from_yaml(yaml_file=yaml_file, name=name, namespace=namespace)
        # `with_mdb_version_from_env` flag enables to skip the custom version setting for class inheriting from MongoDB
        # for example, community must not have an enterprise version set, but we can inherit the from_yaml (itself
        # inherited from CustomObject class
        if with_mdb_version_from_env:
            custom_mdb_prev_version = os.getenv("CUSTOM_MDB_VERSION")
            custom_mdb_version = os.getenv("CUSTOM_MDB_VERSION")
            if (
                custom_mdb_prev_version is not None
                and semver.compare(resource.get_version(), custom_mdb_prev_version) < 0
            ):
                resource.set_version(ensure_ent_version(custom_mdb_prev_version))
            elif custom_mdb_version is not None and semver.compare(resource.get_version(), custom_mdb_version) < 0:
                resource.set_version(ensure_ent_version(custom_mdb_version))
        return resource

    def assert_state_transition_happens(self, last_transition, timeout=None):
        def transition_changed(mdb: MongoDB):
            return mdb.get_status_last_transition_time() != last_transition

        self.wait_for(transition_changed, timeout, should_raise=True)

    def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False):
        intermediate_events = (
            "haven't reached READY",
            "Some agents failed to register",
            # Sometimes Cloud-QA timeouts so we anticipate to this
            "Error sending GET request to",
            # "Get https://cloud-qa.mongodb.com/api/public/v1.0/groups/5f186b406c835e37e6160aef/automationConfig:
            # read tcp 10.244.0.6:33672->75.2.105.99:443: read: connection reset by peer"
            "read: connection reset by peer",
            # Ops Manager must be recovering from an Upgrade, and it is
            # currently DOWN.
            "connect: connection refused",
            "MongoDB version information is not yet available",
            # Enabling authentication is a lengthy process where the agents might not reach READY in time.
            # That can cause a failure and a restart of the reconcile.
            "Failed to enable Authentication",
            # Sometimes agents need longer to register with OM.
            "some agents failed to register or the Operator",
        )

        start_time = time.time()

        self.wait_for(
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

        end_time = time.time()
        span = trace.get_current_span()
        span.set_attribute("mck.resource", self.__class__.__name__)
        span.set_attribute("mck.action", "assert_phase")
        span.set_attribute("mck.desired_phase", phase.name)
        span.set_attribute("mck.time_needed", end_time - start_time)
        logger.debug(
            f"Reaching phase {phase.name} for resource {self.__class__.__name__} took {end_time - start_time}s"
        )

    def assert_abandons_phase(self, phase: Phase, timeout=None):
        """This method can be racy by nature, it assumes that the operator is slow enough that its phase transition
        happens during the time we call this method. If there is not a lot of work, then the phase can already finished
        transitioning during the modification call before calling this method.
        """
        start_time = time.time()
        self.wait_for(lambda s: s.get_status_phase() != phase, timeout, should_raise=True)
        end_time = time.time()
        logger.debug(
            f"Abandonning phase {phase.name} for resource {self.__class__.__name__} took {end_time - start_time}s"
        )

    def assert_backup_reaches_status(self, expected_status: str, timeout: int = 600):
        def reaches_backup_status(mdb: MongoDB) -> bool:
            try:
                return mdb["status"]["backup"]["statusName"] == expected_status
            except KeyError:
                return False

        self.wait_for(reaches_backup_status, timeout=timeout, should_raise=True)

    def assert_status_resource_not_ready(self, name: str, kind: str = "StatefulSet", msg_regexp=None, idx=0):
        """Checks the element in 'resources_not_ready' field by index 'idx'"""
        assert self.get_status_resources_not_ready()[idx]["kind"] == kind
        assert self.get_status_resources_not_ready()[idx]["name"] == name
        assert re.search(msg_regexp, self.get_status_resources_not_ready()[idx]["message"]) is not None

    @property
    def type(self) -> str:
        return self["spec"]["type"]

    def tester(
        self,
        ca_path: Optional[str] = None,
        srv: bool = False,
        use_ssl: Optional[bool] = None,
        service_names: list[str] = None,
    ):
        """Returns a Tester instance for this type of deployment."""
        if self.type == "ReplicaSet" and "clusterSpecList" in self["spec"]:
            raise ValueError("A MongoDB class is being used to represent a MongoDBMulti instance!")

        if self.type == "ReplicaSet":
            return ReplicaSetTester(
                mdb_resource_name=self.name,
                replicas_count=self["status"]["members"],
                ssl=self.is_tls_enabled() if use_ssl is None else use_ssl,
                srv=srv,
                ca_path=ca_path,
                namespace=self.namespace,
                external_domain=self.get_external_domain(),
                cluster_domain=self.get_cluster_domain(),
            )
        elif self.type == "ShardedCluster":
            return ShardedClusterTester(
                mdb_resource_name=self.name,
                mongos_count=self["spec"].get("mongosCount", 0),
                ssl=self.is_tls_enabled() if use_ssl is None else use_ssl,
                srv=srv,
                ca_path=ca_path,
                namespace=self.namespace,
                cluster_domain=self.get_cluster_domain(),
                multi_cluster=self.is_multicluster(),
                service_names=service_names,
                external_domain=self.get_external_domain(),
            )
        elif self.type == "Standalone":
            return StandaloneTester(
                mdb_resource_name=self.name,
                ssl=self.is_tls_enabled() if use_ssl is None else use_ssl,
                ca_path=ca_path,
                namespace=self.namespace,
                external_domain=self.get_external_domain(),
                cluster_domain=self.get_cluster_domain(),
            )

    def assert_connectivity(self, ca_path: Optional[str] = None, cluster_domain: str = "cluster.local"):
        return self.tester(ca_path=ca_path).assert_connectivity()

    def set_architecture_annotation(self):
        if "annotations" not in self["metadata"]:
            self["metadata"]["annotations"] = {}
        if is_default_architecture_static():
            self["metadata"]["annotations"].update({"mongodb.com/v1.architecture": "static"})
        else:
            self["metadata"]["annotations"].update({"mongodb.com/v1.architecture": "non-static"})

    def trigger_architecture_migration(self):
        self.load()
        if "annotations" not in self["metadata"]:
            self["metadata"]["annotations"] = {}
        if is_default_architecture_static():
            self["metadata"]["annotations"].update({"mongodb.com/v1.architecture": "non-static"})
            self.update()
        else:
            self["metadata"]["annotations"].update({"mongodb.com/v1.architecture": "static"})
            self.update()

    def assert_connectivity_from_connection_string(self, cnx_string: str, tls: bool, ca_path: Optional[str] = None):
        """
        Tries to connect to a database using a connection string only.
        """
        return MongoTester(cnx_string, tls, ca_path).assert_connectivity()

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDB ({})| status: {}| message: {}".format(
            self.name, self.get_status_phase(), self.get_status_message()
        )

    def configure(
        self,
        om: Optional[MongoDBOpsManager],
        project_name: Optional[str] = None,
        ca_config_map_name: Optional[str] = None,
        api_client: Optional[client.ApiClient] = None,
    ) -> MongoDB:
        self.api = kubernetes.client.CustomObjectsApi(api_client)
        if project_name is None:
            project_name = self.name

        if om is not None:
            return self.configure_ops_manager(
                om, project_name, ca_config_map_name=ca_config_map_name, api_client=api_client
            )
        else:
            return self.configure_cloud_qa(project_name, api_client=api_client)

    def configure_ops_manager(
        self,
        om: Optional[MongoDBOpsManager],
        project_name: str,
        ca_config_map_name: Optional[str] = None,
        api_client: Optional[client.ApiClient] = None,
    ) -> MongoDB:
        if "project" in self["spec"]:
            del self["spec"]["project"]

        ensure_nested_objects(self, ["spec", "opsManager", "configMapRef"])

        if ca_config_map_name is None:
            ca_config_map_name = self["spec"].get("security", {}).get("tls", {}).get("ca", None)

        self["spec"]["opsManager"]["configMapRef"]["name"] = om.get_or_create_mongodb_connection_config_map(
            self.name, project_name, self.namespace, ca_config_map_name=ca_config_map_name, api_client=api_client
        )
        # Note that if the MongoDB object is created in a different namespace than the Operator
        # then the secret needs to be copied there manually
        self["spec"]["credentials"] = om.api_key_secret(self.namespace, api_client=api_client)

        return self

    def configure_cloud_qa(
        self,
        project_name,
        api_client: Optional[client.ApiClient] = None,
    ) -> MongoDB:
        if "opsManager" in self["spec"]:
            del self["spec"]["opsManager"]

        src_cm_name = self.config_map_name
        if src_cm_name is None:
            src_cm_name = "my-project"

        src_cm = self.read_configmap(config_map_name=src_cm_name, api_client=api_client)

        new_project_config_map_name = f"{self.name}-project-config"
        ensure_nested_objects(self, ["spec", "cloudManager", "configMapRef"])
        self["spec"]["cloudManager"]["configMapRef"]["name"] = new_project_config_map_name

        # we update the project name by adding a namespace prefix to ensure uniqueness in shared cloud-qa projects
        # the namespace prefix is not added if we run the test against
        src_cm.update({"projectName": f"{self.namespace}-{project_name}"})
        create_or_update_configmap(self.namespace, new_project_config_map_name, src_cm, api_client=api_client)

        return self

    def get_om_project_name(self) -> str:
        project_cm = self.read_configmap(self.config_map_name)
        return project_cm["projectName"]

    def configure_backup(self, mode: str = "enabled") -> MongoDB:
        ensure_nested_objects(self, ["spec", "backup"])
        self["spec"]["backup"]["mode"] = mode
        return self

    def configure_custom_tls(
        self,
        issuer_ca_configmap_name: str,
        tls_cert_secret_name: str,
    ):
        ensure_nested_objects(self, ["spec", "security", "tls"])
        self["spec"]["security"]["certsSecretPrefix"] = tls_cert_secret_name
        self["spec"]["security"]["tls"].update({"enabled": True, "ca": issuer_ca_configmap_name})

    def build_list_of_hosts(self):
        """Returns the list of full_fqdn:27017 for every member of the mongodb resource"""
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
        return client.AppsV1Api().read_namespaced_stateful_set(self.name, self.namespace)

    def read_configmap(
        self, config_map_name: Optional[str], api_client: Optional[client.ApiClient] = None
    ) -> Optional[Dict[str, str]]:
        if config_map_name is None:
            raise Exception(
                "Project config map is empty. Modify resource yaml or use configure method to set spec.opsManager or spec.cloudManager fields."
            )
        return KubernetesTester.read_configmap(self.namespace, config_map_name, api_client=api_client)

    def mongo_uri(self, user_name: Optional[str] = None, password: Optional[str] = None) -> str:
        """Returns the mongo uri for the MongoDB resource. The logic matches the one in 'types.go'"""
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

        query_params = ["{}={}".format(key, params[key]) for key in sorted(params.keys())]
        joined_params = "&".join(query_params)
        return proto + auth + hosts + "/?" + joined_params

    def get_members(self) -> int:
        return self["spec"]["members"]

    def get_version(self) -> str:
        try:
            return self["spec"]["version"]
        except KeyError:
            custom_mdb_version = os.getenv("CUSTOM_MDB_VERSION", "6.0.10")
            return custom_mdb_version

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
        return self

    def get_authentication(self) -> Optional[Dict]:
        try:
            return self["spec"]["security"]["authentication"]
        except KeyError:
            return {}

    def get_oidc_provider_configs(self) -> Optional[Dict]:
        try:
            return self["spec"]["security"]["authentication"]["oidcProviderConfigs"]
        except KeyError:
            return {}

    def set_oidc_provider_configs(self, oidc_provider_configs: Dict):
        self["spec"]["security"]["authentication"]["oidcProviderConfigs"] = oidc_provider_configs
        return self

    def append_oidc_provider_config(self, new_config: Dict):
        if "oidcProviderConfigs" not in self["spec"]["security"]["authentication"]:
            self["spec"]["security"]["authentication"]["oidcProviderConfigs"] = []
        self["spec"]["security"]["authentication"]["oidcProviderConfigs"].append(new_config)

        return self

    def get_roles(self) -> Optional[Dict]:
        try:
            return self["spec"]["security"]["roles"]
        except KeyError:
            return {}

    def append_role(self, new_role: Dict):
        if "roles" not in self["spec"]["security"]:
            self["spec"]["security"]["roles"] = []
        self["spec"]["security"]["roles"].append(new_role)

        return self

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

    def get_status_fcv(self) -> Optional[str]:
        try:
            return self["status"]["featureCompatibilityVersion"]
        except KeyError:
            return None

    def get_status_last_transition_time(self) -> Optional[str]:
        return self["status"]["lastTransition"]

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
        """Returns the OMTester instance based on MongoDB connectivity parameters"""
        config_map = self.read_configmap(self.config_map_name)
        secret = KubernetesTester.read_secret(self.namespace, self["spec"]["credentials"])
        return OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))

    def get_automation_config_tester(self, **kwargs):
        """This is just a shortcut for getting automation config tester for replica set"""
        if "group_name" not in kwargs:
            kwargs["group_name"] = self.get_om_project_name()
        return self.get_om_tester().get_automation_config_tester(**kwargs)

    def get_external_domain(self):
        multi_cluster_external_domain = (
            self["spec"]
            .get("mongos", {})
            .get("clusterSpecList", [{}])[0]
            .get("externalAccess", {})
            .get("externalDomain", None)
        )
        return self["spec"].get("externalAccess", {}).get("externalDomain", None) or multi_cluster_external_domain

    @property
    def config_map_name(self) -> Optional[str]:
        if "opsManager" in self["spec"]:
            return self["spec"]["opsManager"]["configMapRef"]["name"]
        elif "cloudManager" in self["spec"]:
            return self["spec"]["cloudManager"]["configMapRef"]["name"]

        return self["spec"].get("project", None)

    def shard_replicaset_names(self) -> List[str]:
        return ["{}-{}".format(self.name, i) for i in range(1, self["spec"]["shardCount"])]

    def shard_statefulset_name(self, shard_idx: int, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-{shard_idx}-{cluster_idx}"
        return f"{self.name}-{shard_idx}"

    def shard_pod_name(self, shard_idx: int, member_idx: int, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-{shard_idx}-{cluster_idx}-{member_idx}"
        return f"{self.name}-{shard_idx}-{member_idx}"

    def shard_service_name(self, shard_idx: Optional[int] = None, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-{shard_idx}-{cluster_idx}-svc"
        else:
            return f"{self.name}-sh"

    def shard_hostname(
        self, shard_idx: int, member_idx: int, cluster_idx: Optional[int] = None, port: int = 27017
    ) -> str:
        if self.is_multicluster():
            return f"{self.name}-{shard_idx}-{cluster_idx}-{member_idx}-svc.{self.namespace}.svc.cluster.local:{port}"
        return f"{self.name}-{shard_idx}-{member_idx}.{self.name}-sh.{self.namespace}.svc.cluster.local:{port}"

    def shard_pvc_name(self, shard_idx: int, member_idx: int, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"data-{self.name}-{shard_idx}-{cluster_idx}-{member_idx}"
        return f"data-{self.name}-{shard_idx}-{member_idx}"

    def shard_members_in_cluster(self, cluster_name: str) -> int:
        if "shardOverrides" in self["spec"]:
            raise Exception("Shard overrides logic is not supported")

        if self.is_multicluster():
            for cluster_spec_item in self["spec"]["shard"]["clusterSpecList"]:
                if cluster_spec_item["clusterName"] == cluster_name:
                    return cluster_spec_item["members"]

        return self["spec"].get("mongodsPerShardCount", 0)

    def config_srv_statefulset_name(self, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-config-{cluster_idx}"
        return f"{self.name}-config"

    def config_srv_replicaset_name(self) -> str:
        return f"{self.name}-config"

    def config_srv_pod_name(self, member_idx: int, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-config-{cluster_idx}-{member_idx}"
        return f"{self.name}-config-{member_idx}"

    def config_srv_members_in_cluster(self, cluster_name: str) -> int:
        if self.is_multicluster():
            for cluster_spec_item in self["spec"]["configSrv"]["clusterSpecList"]:
                if cluster_spec_item["clusterName"] == cluster_name:
                    return cluster_spec_item["members"]

        return self["spec"].get("configServerCount", 0)

    def config_srv_pvc_name(self, member_idx: int, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"data-{self.name}-config-{cluster_idx}-{member_idx}"
        return f"data-{self.name}-config-{member_idx}"

    def mongos_statefulset_name(self, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-mongos-{cluster_idx}"
        return f"{self.name}-mongos"

    def mongos_pod_name(self, member_idx: int, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-mongos-{cluster_idx}-{member_idx}"
        return f"{self.name}-mongos-{member_idx}"

    def mongos_hostname(self, member_idx: Optional[int] = None, cluster_idx: Optional[int] = None) -> str:
        service_name = self.mongos_service_name(member_idx, cluster_idx)
        if self.is_multicluster():
            return f"{service_name}.{self.namespace}.svc.cluster.local"

        return f"{self.mongos_pod_name(member_idx, cluster_idx)}.{service_name}.{self.namespace}.svc.cluster.local"

    def mongos_service_name(self, member_idx: Optional[int] = None, cluster_idx: Optional[int] = None) -> str:
        if self.is_multicluster():
            return f"{self.name}-mongos-{cluster_idx}-{member_idx}-svc"
        else:
            return f"{self.name}-svc"

    def mongos_members_in_cluster(self, cluster_name: str) -> int:
        if self.is_multicluster():
            for cluster_spec_item in self["spec"]["mongos"]["clusterSpecList"]:
                if cluster_spec_item["clusterName"] == cluster_name:
                    return cluster_spec_item["members"]

        return self["spec"].get("mongosCount", 0)

    def is_multicluster(self) -> bool:
        return self["spec"].get("topology", None) == "MultiCluster"

    class Types:
        REPLICA_SET = "ReplicaSet"
        SHARDED_CLUSTER = "ShardedCluster"
        STANDALONE = "Standalone"
