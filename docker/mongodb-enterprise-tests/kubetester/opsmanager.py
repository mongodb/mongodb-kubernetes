from __future__ import annotations

import json
import os
from typing import List, Optional

from kubeobject import CustomObject
from kubernetes import client
from kubernetes.client.rest import ApiException

from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, build_list_of_hosts, decode_secret
from kubetester.mongodb import MongoDBCommon, Phase, in_desired_state, MongoDB, get_pods
from kubetester.mongotester import ReplicaSetTester
from kubetester.omtester import OMTester, OMContext


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

    def appdb_status(self) -> MongoDBOpsManager.AppDbStatus:
        return self.AppDbStatus(self)

    def om_status(self) -> MongoDBOpsManager.OmStatus:
        return self.OmStatus(self)

    def backup_status(self) -> MongoDBOpsManager.BackupStatus:
        return self.BackupStatus(self)

    def assert_reaches(self, fn, timeout=None):
        return self.wait_for(fn, timeout=timeout, should_raise=True)

    def get_appdb_resource(self) -> MongoDB:
        mdb = MongoDB(name=self.app_db_name(), namespace=self.namespace)
        # We "artificially" add SCRAM authentication to make syntax match the normal MongoDB -
        # this will let the mongo_uri() method work correctly
        # (opsmanager_types.go does the same)
        mdb["spec"] = self["spec"]["applicationDatabase"]
        mdb["spec"]["type"] = MongoDB.Types.REPLICA_SET
        mdb["spec"]["security"] = {"authentication": {"modes": ["SCRAM"]}}
        return mdb

    def services(self) -> List[client.V1Service]:
        """Returns a two element list with internal and external Services.

        Any of them might be None if the Service is not found.
        """
        services = []
        service_names = (self.svc_name(), self.external_svc_name())

        for name in service_names:
            try:
                svc = client.CoreV1Api().read_namespaced_service(name, self.namespace)
                services.append(svc)
            except ApiException:
                services.append(None)

        return services[0], services[1]

    def read_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.name, self.namespace
        )

    def read_appdb_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.app_db_name(), self.namespace
        )

    def read_backup_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.backup_daemon_name(), self.namespace
        )

    def read_om_pods(self) -> List[client.V1Pod]:
        return [
            client.CoreV1Api().read_namespaced_pod(podname, self.namespace)
            for podname in get_pods(self.name + "-{}", self.get_replicas())
        ]

    def read_appdb_pods(self) -> List[client.V1Pod]:
        return [
            client.CoreV1Api().read_namespaced_pod(podname, self.namespace)
            for podname in get_pods(
                self.app_db_name() + "-{}", self.get_appdb_members_count()
            )
        ]

    def read_backup_pod(self) -> client.V1Pod:
        return client.CoreV1Api().read_namespaced_pod(
            self.backup_daemon_pod_name(), self.namespace
        )

    def read_gen_key_secret(self) -> client.V1Secret:
        return client.CoreV1Api().read_namespaced_secret(
            self.name + "-gen-key", self.namespace
        )

    def read_api_key_secret(self) -> client.V1Secret:
        return client.CoreV1Api().read_namespaced_secret(
            self.api_key_secret(), self.namespace
        )

    def read_appdb_generated_password_secret(self) -> client.V1Secret:
        return client.CoreV1Api().read_namespaced_secret(
            self.app_db_name() + "-password", self.namespace
        )

    def read_appdb_generated_password(self) -> str:
        data = self.read_appdb_generated_password_secret().data
        return KubernetesTester.decode_secret(data)["password"]

    def get_automation_config_tester(self, **kwargs) -> AutomationConfigTester:
        cm = (
            client.CoreV1Api()
            .read_namespaced_config_map(self.app_db_name() + "-config", self.namespace)
            .data
        )
        automation_config_str = cm["cluster-config.json"]
        config_json = json.loads(automation_config_str)
        return AutomationConfigTester(config_json, **kwargs)

    def get_or_create_mongodb_connection_config_map(
        self, mongodb_name: str, project_name: str
    ) -> str:
        config_map_name = f"{mongodb_name}-config"
        data = {"baseUrl": self.om_status().get_url(), "projectName": project_name}

        try:
            KubernetesTester.create_configmap(
                self.namespace, config_map_name, data,
            )
        except ApiException as e:
            if e.status != 409:
                raise

            # If the ConfigMap already exist, it will be updated with
            # an updated status_url()
            KubernetesTester.update_configmap(self.namespace, config_map_name, data)

        return config_map_name

    def get_om_tester(self, project_name: Optional[str] = None) -> OMTester:
        """ Returns the instance of OMTester helping to check the state of Ops Manager deployed in Kubernetes. """
        api_key_secret = KubernetesTester.read_secret(
            KubernetesTester.get_namespace(), self.api_key_secret()
        )
        om_context = OMContext(
            self.om_status().get_url(),
            api_key_secret["user"],
            api_key_secret["publicApiKey"],
            project_name=project_name,
        )
        return OMTester(om_context)

    def get_appdb_tester(self, **kwargs) -> ReplicaSetTester:
        return ReplicaSetTester(
            self.app_db_name(),
            replicas_count=self.appdb_status().get_members(),
            **kwargs,
        )

    def pod_urls(self):
        """ Returns http urls to each pod in the Ops Manager """
        return [
            "http://{}".format(host)
            for host in build_list_of_hosts(
                self.name, self.namespace, self.get_replicas(), port=8080
            )
        ]

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDBOpsManager| status:".format(self.get_status())

    def get_appdb_members_count(self) -> int:
        return self["spec"]["applicationDatabase"]["members"]

    def get_replicas(self) -> int:
        return self["spec"]["replicas"]

    def get_status(self) -> Optional:
        if "status" not in self:
            return None
        return self["status"]

    def api_key_secret(self) -> str:
        return self.name + "-admin-key"

    def app_db_name(self) -> str:
        return self.name + "-db"

    def app_db_password_secret_name(self) -> str:
        return self.app_db_name() + "-password"

    def backup_daemon_name(self) -> str:
        return self.name + "-backup-daemon"

    def backup_daemon_pod_name(self) -> str:
        return self.backup_daemon_name() + "-0"

    def svc_name(self) -> str:
        return self.name + "-svc"

    def external_svc_name(self) -> str:
        return self.name + "-svc-ext"

    class StatusCommon:
        def assert_reaches_phase(
            self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False,
        ):
            self.ops_manager.wait_for(
                lambda s: in_desired_state(
                    self.get_phase(),
                    phase,
                    self.get_message(),
                    msg_regexp=msg_regexp,
                    ignore_errors=ignore_errors,
                ),
                timeout,
                should_raise=True,
            )

        def assert_abandons_phase(self, phase: Phase, timeout=None):
            return self.ops_manager.wait_for(
                lambda s: self.get_phase() != phase, timeout, should_raise=True
            )

    class BackupStatus(StatusCommon):
        def __init__(self, ops_manager: MongoDBOpsManager):
            self.ops_manager = ops_manager

        def get_phase(self) -> Optional[Phase]:
            try:
                return Phase[self.ops_manager.get_status()["backup"]["phase"]]
            except (KeyError, TypeError):
                return None

        def get_message(self) -> Optional[str]:
            try:
                return self.ops_manager.get_status()["backup"]["message"]
            except (KeyError, TypeError):
                return None

        def assert_reaches_phase(
            self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False,
        ):
            super().assert_reaches_phase(
                phase,
                msg_regexp=msg_regexp,
                timeout=timeout,
                ignore_errors=ignore_errors,
            )
            # If backup is Running other statuses must be Running as well
            if phase == Phase.Running:
                assert self.ops_manager.om_status().get_phase() == Phase.Running
                assert self.ops_manager.appdb_status().get_phase() == Phase.Running

    class AppDbStatus(StatusCommon):
        def __init__(self, ops_manager: MongoDBOpsManager):
            self.ops_manager = ops_manager

        def get_phase(self) -> Optional[Phase]:
            try:
                return Phase[
                    self.ops_manager.get_status()["applicationDatabase"]["phase"]
                ]
            except (KeyError, TypeError):
                return None

        def get_message(self) -> Optional[str]:
            try:
                return self.ops_manager.get_status()["applicationDatabase"]["message"]
            except (KeyError, TypeError):
                return None

        def get_version(self) -> Optional[str]:
            try:
                return self.ops_manager.get_status()["applicationDatabase"]["version"]
            except (KeyError, TypeError):
                return None

        def get_members(self) -> Optional[int]:
            try:
                return self.ops_manager.get_status()["applicationDatabase"]["members"]
            except (KeyError, TypeError):
                return None

    class OmStatus(StatusCommon):
        def __init__(self, ops_manager: MongoDBOpsManager):
            self.ops_manager = ops_manager

        def get_phase(self) -> Optional[Phase]:
            try:
                return Phase[self.ops_manager.get_status()["opsManager"]["phase"]]
            except (KeyError, TypeError):
                return None

        def get_message(self) -> Optional[str]:
            try:
                return self.ops_manager.get_status()["opsManager"]["message"]
            except (KeyError, TypeError):
                return None

        def get_last_transition(self) -> Optional[int]:
            try:
                return self.ops_manager.get_status()["opsManager"]["lastTransition"]
            except (KeyError, TypeError):
                return None

        def get_url(self) -> Optional[str]:
            try:
                return self.ops_manager.get_status()["opsManager"]["url"]
            except (KeyError, TypeError):
                return None

        def get_replicas(self) -> Optional[int]:
            try:
                return self.ops_manager.get_status()["opsManager"]["replicas"]
            except (KeyError, TypeError):
                return None

    @staticmethod
    def get_bundled_appdb_version() -> str:
        version = os.getenv("BUNDLED_APP_DB_VERSION", None)
        if version is None:
            raise ValueError("BUNDLED_APP_DB_VERSION needs to be defined")
        return version.partition("-")[0]
