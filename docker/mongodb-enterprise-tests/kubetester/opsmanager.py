from __future__ import annotations

import json
import os
import re
from typing import List, Optional, Dict, Callable
from base64 import b64decode
from kubeobject import CustomObject
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, build_list_of_hosts
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

    def assert_reaches(self, fn: Callable[[MongoDBOpsManager], bool], timeout=None):
        return self.wait_for(fn, timeout=timeout, should_raise=True)

    def get_appdb_hosts(self):
        tester = self.get_om_tester(self.app_db_name())
        tester.assert_group_exists()
        return tester.api_get_hosts()["results"]

    def assert_appdb_monitoring_group_was_created(self):
        tester = self.get_om_tester(self.app_db_name())
        tester.assert_group_exists()
        hosts = tester.api_get_hosts()["results"]

        appdb_resource = self.get_appdb_resource()
        resource_name = appdb_resource["metadata"]["name"]
        service_name = f"{resource_name}-svc"
        namespace = appdb_resource["metadata"]["namespace"]

        appdb_hostnames = []
        for index in range(appdb_resource["spec"]["members"]):
            appdb_hostnames.append(
                f"{resource_name}-{index}.{service_name}.{namespace}.svc.cluster.local"
            )

        def agents_have_registered() -> bool:
            monitoring_agents = tester.api_read_monitoring_agents()
            expected_number_of_agents_in_standby = (
                len(
                    [
                        agent
                        for agent in monitoring_agents
                        if agent["stateName"] == "STANDBY"
                    ]
                )
                == self.get_appdb_members_count() - 1
            )
            expected_number_of_agents_are_active = (
                len(
                    [
                        agent
                        for agent in monitoring_agents
                        if agent["stateName"] == "ACTIVE"
                    ]
                )
                == 1
            )
            return (
                expected_number_of_agents_in_standby
                and expected_number_of_agents_are_active
            )

        KubernetesTester.wait_until(agents_have_registered, timeout=20, sleep_time=5)

        registered_automation_agents = tester.api_read_automation_agents()
        assert len(registered_automation_agents) == 0

        registered_agents = tester.api_read_monitoring_agents()
        hostnames = [host["hostname"] for host in hosts]
        for hn in appdb_hostnames:
            assert hn in hostnames

        for ra in registered_agents:
            assert ra["hostname"] in appdb_hostnames

    def get_appdb_resource(self) -> MongoDB:
        mdb = MongoDB(name=self.app_db_name(), namespace=self.namespace)
        # We "artificially" add SCRAM authentication to make syntax match the normal MongoDB -
        # this will let the mongo_uri() method work correctly
        # (opsmanager_types.go does the same)
        mdb["spec"] = self["spec"]["applicationDatabase"]
        mdb["spec"]["type"] = MongoDB.Types.REPLICA_SET
        mdb["spec"]["security"] = {"authentication": {"modes": ["SCRAM"]}}
        return mdb

    def services(self) -> List[Optional[client.V1Service]]:
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

        return [services[0], services[1]]

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

    def wait_until_backup_pod_becomes_ready(self, timeout=300):
        def backup_daemon_is_ready():
            try:
                backup_pod = self.read_backup_pod()
                return backup_pod.status.container_statuses[0].ready
            except Exception as e:
                print("Error checking if pod is ready: " + str(e))
                return False

        KubernetesTester.wait_until(backup_daemon_is_ready, timeout=timeout)

    def read_gen_key_secret(self) -> client.V1Secret:
        return client.CoreV1Api().read_namespaced_secret(
            self.name + "-gen-key", self.namespace
        )

    def read_api_key_secret(self, namespace=None) -> client.V1Secret:
        """Reads the API key secret for the global admin created by the Operator. Note, that the secret is
        located in the Operator namespace - not Ops Manager one, so the 'namespace' parameter must be passed
        if the Ops Manager is installed in a separate namespace"""
        if namespace is None:
            namespace = self.namespace
        return client.CoreV1Api().read_namespaced_secret(
            self.api_key_secret(namespace), namespace
        )

    def read_appdb_generated_password_secret(self) -> client.V1Secret:
        return client.CoreV1Api().read_namespaced_secret(
            self.app_db_name() + "-om-password", self.namespace
        )

    def read_appdb_generated_password(self) -> str:
        data = self.read_appdb_generated_password_secret().data
        return KubernetesTester.decode_secret(data)["password"]

    def create_admin_secret(
        self,
        user_name="jane.doe@example.com",
        password="Passw0rd.",
        first_name="Jane",
        last_name="Doe",
    ):
        data = {
            "Username": user_name,
            "Password": password,
            "FirstName": first_name,
            "LastName": last_name,
        }
        KubernetesTester.create_secret(
            self.namespace, self.get_admin_secret_name(), data
        )

    def get_automation_config_tester(self, **kwargs) -> AutomationConfigTester:
        secret = (
            client.CoreV1Api()
            .read_namespaced_secret(self.app_db_name() + "-config", self.namespace)
            .data
        )
        automation_config_str = b64decode(secret["cluster-config.json"]).decode("utf-8")
        config_json = json.loads(automation_config_str)
        return AutomationConfigTester(config_json, **kwargs)

    def get_or_create_mongodb_connection_config_map(
        self, mongodb_name: str, project_name: str, namespace=None
    ) -> str:
        config_map_name = f"{mongodb_name}-config"
        data = {"baseUrl": self.om_status().get_url(), "projectName": project_name}

        # the namespace can be different from OM one if the MongoDB is created in a separate namespace
        if namespace is None:
            namespace = self.namespace
        try:
            KubernetesTester.create_configmap(
                namespace,
                config_map_name,
                data,
            )
        except ApiException as e:
            if e.status != 409:
                raise

            # If the ConfigMap already exist, it will be updated with
            # an updated status_url()
            KubernetesTester.update_configmap(namespace, config_map_name, data)

        return config_map_name

    def get_om_tester(self, project_name: Optional[str] = None) -> OMTester:
        """ Returns the instance of OMTester helping to check the state of Ops Manager deployed in Kubernetes. """
        api_key_secret = KubernetesTester.read_secret(
            KubernetesTester.get_namespace(),
            self.api_key_secret(KubernetesTester.get_namespace()),
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

    def set_version(self, version: Optional[str]):
        """Sets a specific `version` if set. If `version` is None, then skip."""
        if version is not None:
            self["spec"]["version"] = version

    def set_appdb_version(self, version: str):
        self["spec"]["applicationDatabase"]["version"] = version

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDBOpsManager| status:".format(self.get_status())

    def get_appdb_members_count(self) -> int:
        return self["spec"]["applicationDatabase"]["members"]

    def get_appdb_connection_url_secret_name(self):
        return f"{self.app_db_name()}-connection-string"

    def get_replicas(self) -> int:
        return self["spec"]["replicas"]

    def get_admin_secret_name(self) -> str:
        return self["spec"]["adminCredentials"]

    def get_version(self) -> str:
        return self["spec"]["version"]

    def get_status(self) -> Optional:
        if "status" not in self:
            return None
        return self["status"]

    def api_key_secret(self, namespace=None) -> str:
        old_secret_name = self.name + "-admin-key"

        # try to read the old secret, if it's is present return it, else return the new secret name
        try:
            client.CoreV1Api().read_namespaced_secret(old_secret_name, namespace)
        except ApiException as e:
            if e.status == 404:
                return "{}-{}-admin-key".format(self.namespace, self.name)

        return old_secret_name

    def app_db_name(self) -> str:
        return self.name + "-db"

    def app_db_password_secret_name(self) -> str:
        return self.app_db_name() + "-om-user-password"

    def backup_daemon_name(self) -> str:
        return self.name + "-backup-daemon"

    def backup_daemon_pod_name(self) -> str:
        return self.backup_daemon_name() + "-0"

    def svc_name(self) -> str:
        return self.name + "-svc"

    def external_svc_name(self) -> str:
        return self.name + "-svc-ext"

    def download_mongodb_binaries(self, version: str):
        """ Downloads mongodb binary in each OM pod, optional downloads MongoDB Tools """
        distros = [
            f"mongodb-linux-x86_64-rhel80-{version}.tgz",
            f"mongodb-linux-x86_64-ubuntu1604-{version}.tgz",
        ]

        for pod in self.read_om_pods():
            for distro in distros:
                cmd = [
                    "curl",
                    "-L",
                    f"https://fastdl.mongodb.org/linux/{distro}",
                    "-o",
                    f"/mongodb-ops-manager/mongodb-releases/{distro}",
                ]

                KubernetesTester.run_command_in_pod_container(
                    pod.metadata.name, self.namespace, cmd
                )

    class StatusCommon:
        def assert_reaches_phase(
            self,
            phase: Phase,
            msg_regexp=None,
            timeout=None,
            ignore_errors=False,
        ):
            self.ops_manager.wait_for(
                lambda s: in_desired_state(
                    current_state=self.get_phase(),
                    desired_state=phase,
                    current_generation=self.ops_manager.get_generation(),
                    observed_generation=self.get_observed_generation(),
                    current_message=self.get_message(),
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

        def assert_status_resource_not_ready(
            self, name: str, kind: str = "StatefulSet", msg_regexp=None, idx=0
        ):
            """Checks the element in 'resources_not_ready' field by index 'idx' """
            assert self.get_resources_not_ready()[idx]["kind"] == kind
            assert self.get_resources_not_ready()[idx]["name"] == name
            assert (
                re.search(msg_regexp, self.get_resources_not_ready()[idx]["message"])
                is not None
            )

        def assert_empty_status_resources_not_ready(self):
            assert self.get_resources_not_ready() is None

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

        def get_observed_generation(self) -> Optional[int]:
            try:
                return self.ops_manager.get_status()["backup"]["observedGeneration"]
            except (KeyError, TypeError):
                return None

        def get_resources_not_ready(self) -> Optional[List[Dict]]:
            try:
                return self.ops_manager.get_status()["backup"]["resourcesNotReady"]
            except (KeyError, TypeError):
                return None

        def assert_reaches_phase(
            self,
            phase: Phase,
            msg_regexp=None,
            timeout=None,
            ignore_errors=False,
        ):
            super().assert_reaches_phase(
                phase,
                msg_regexp=msg_regexp,
                timeout=timeout,
                ignore_errors=ignore_errors,
            )
            # If backup is Running other statuses must be Running as well
            # So far let's comment this as sometimes there are some extra reconciliations happening
            # (doing no work at all) without known reasons for
            # if phase == Phase.Running:
            #     assert self.ops_manager.om_status().get_phase() == Phase.Running
            #     assert self.ops_manager.appdb_status().get_phase() == Phase.Running

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

        def get_observed_generation(self) -> Optional[int]:
            try:
                return self.ops_manager.get_status()["applicationDatabase"][
                    "observedGeneration"
                ]
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

        def get_resources_not_ready(self) -> Optional[List[Dict]]:
            try:
                return self.ops_manager.get_status()["applicationDatabase"][
                    "resourcesNotReady"
                ]
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

        def get_observed_generation(self) -> Optional[int]:
            try:
                return self.ops_manager.get_status()["opsManager"]["observedGeneration"]
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

        def get_resources_not_ready(self) -> Optional[List[Dict]]:
            try:
                return self.ops_manager.get_status()["opsManager"]["resourcesNotReady"]
            except (KeyError, TypeError):
                return None
