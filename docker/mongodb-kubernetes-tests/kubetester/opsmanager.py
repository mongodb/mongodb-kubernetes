from __future__ import annotations

import json
import re
import time
from base64 import b64decode
from typing import Callable, Dict, List, Optional

import kubernetes.client
import requests
from kubeobject import CustomObject
from kubernetes.client.rest import ApiException
from kubetester import (
    create_configmap,
    create_or_update_configmap,
    create_or_update_secret,
    read_secret,
)
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import (
    KubernetesTester,
    build_list_of_hosts,
    get_pods,
    is_default_architecture_static,
)
from kubetester.mongodb_common import MongoDBCommon
from kubetester.mongodb_utils_state import in_desired_state
from kubetester.mongotester import MongoTester, MultiReplicaSetTester, ReplicaSetTester
from kubetester.omtester import OMContext, OMTester
from kubetester.phase import Phase
from opentelemetry import trace
from requests.auth import HTTPDigestAuth
from tests import test_logger
from tests.common.multicluster.multicluster_utils import (
    multi_cluster_pod_names,
    multi_cluster_service_names,
)
from tests.conftest import (
    LEGACY_CENTRAL_CLUSTER_NAME,
    get_central_cluster_client,
    get_member_cluster_api_client,
    get_member_cluster_client_map,
    is_member_cluster,
    read_deployment_state,
)

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


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

    def trigger_architecture_migration(self):
        self.load()

        if is_default_architecture_static():
            self["metadata"]["annotations"].update({"mongodb.com/v1.architecture": "non-static"})
            self.update()
        else:
            self["metadata"]["annotations"].update({"mongodb.com/v1.architecture": "static"})
            self.update()

    def trigger_om_sts_restart(self):
        """
        Adds or changes a label from the pod template to trigger a rolling restart of the OpsManager StatefulSet.
        """
        self.load()
        self["spec"]["statefulSet"] = {
            "spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": str(time.time())}}}}
        }
        self.update()

    def trigger_appdb_sts_restart(self):
        """
        Adds or changes a label from the pod template to trigger a rolling restart of the AppDB StatefulSet.
        """
        self.load()
        self["spec"]["applicationDatabase"] = {
            "podSpec": {
                "podTemplate": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": str(time.time())}}}
            }
        }
        self.update()

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

    def get_appdb_preferred_hostnames(self):
        tester = self.get_om_tester(self.app_db_name())
        return tester.api_get_preferred_hostnames()

    def assert_appdb_preferred_hostnames_are_added(self):
        def appdb_preferred_hostnames_are_added():
            expected_hostnames = self.get_appdb_hostnames_for_monitoring()
            preferred_hostnames = self.get_appdb_preferred_hostnames()

            if len(preferred_hostnames) != len(expected_hostnames):
                return False

            for hostname in preferred_hostnames:
                if hostname["value"] not in expected_hostnames:
                    return False
            return True

        KubernetesTester.wait_until(appdb_preferred_hostnames_are_added, timeout=120, sleep_time=5)

    def assert_appdb_hostnames_are_correct(self):
        def appdb_hostnames_are_correct():
            expected_hostnames = self.get_appdb_hostnames_for_monitoring()
            hosts = self.get_appdb_hosts()

            if len(hosts) != len(expected_hostnames):
                return False

            for host in hosts:
                if host["hostname"] not in expected_hostnames:
                    return False
            return True

        KubernetesTester.wait_until(appdb_hostnames_are_correct, timeout=300, sleep_time=10)

    def assert_appdb_monitoring_group_was_created(self):
        tester = self.get_om_tester(self.app_db_name())
        tester.assert_group_exists()

        appdb_hostnames = self.get_appdb_hostnames_for_monitoring()

        def monitoring_agents_have_registered() -> bool:
            monitoring_agents = tester.api_read_monitoring_agents()
            appdb_monitoring_agents = [a for a in monitoring_agents if a["hostname"] in appdb_hostnames]
            expected_number_of_agents = len(appdb_monitoring_agents) == self.get_appdb_members_count()

            expected_number_of_agents_in_standby = (
                len([agent for agent in appdb_monitoring_agents if agent["stateName"] == "STANDBY"])
                == self.get_appdb_members_count() - 1
            )
            expected_number_of_agents_are_active = (
                len([agent for agent in appdb_monitoring_agents if agent["stateName"] == "ACTIVE"]) == 1
            )

            return (
                expected_number_of_agents
                and expected_number_of_agents_in_standby
                and expected_number_of_agents_are_active
            )

        KubernetesTester.wait_until(monitoring_agents_have_registered, timeout=600, sleep_time=5)

        def no_automation_agents_have_registered() -> bool:
            automation_agents = tester.api_read_automation_agents()
            appdb_automation_agents = [a for a in automation_agents if a["hostname"] in appdb_hostnames]
            return len(appdb_automation_agents) == 0

        KubernetesTester.wait_until(no_automation_agents_have_registered, timeout=600, sleep_time=5)

    TRACER.start_as_current_span("assert_monitoring_data_exists")

    def assert_monitoring_data_exists(
        self,
        database_name: Optional[str] = None,
        period: str = "P1DT12H",
        timeout: int = 600,
        all_hosts: bool = True,
    ):
        """
        Asserts the existence of monitoring measurements in this Ops Manager instance.
        """
        appdb_hosts = self.get_appdb_hosts()
        host_ids = [host["id"] for host in appdb_hosts]
        project_id = [host["groupId"] for host in appdb_hosts][0]
        tester = self.get_om_tester()

        def agent_is_showing_metrics():
            for host_id in host_ids:
                measurements = tester.api_read_measurements(
                    host_id,
                    database_name=database_name,
                    project_id=project_id,
                    period=period,
                )
                if measurements is None and all_hosts:
                    return False
                elif measurements is None and not all_hosts:
                    continue

                found = False
                for measurement in measurements:
                    if len(measurement["dataPoints"]) > 0:
                        found = True
                        break

                if all_hosts and not found:
                    return False
                elif not all_hosts and found:
                    return True

            if all_hosts:
                return True
            return False

        KubernetesTester.wait_until(
            agent_is_showing_metrics,
            timeout=timeout,
        )

    def get_appdb_resource(self) -> CustomObject:
        from kubetester.mongodb import MongoDB

        mdb = MongoDB(name=self.app_db_name(), namespace=self.namespace)
        # We "artificially" add SCRAM authentication to make syntax match the normal MongoDB -
        # this will let the mongo_uri() method work correctly
        # (opsmanager_types.go does the same)
        mdb["spec"] = self["spec"]["applicationDatabase"]
        mdb["spec"]["type"] = MongoDB.Types.REPLICA_SET
        mdb["spec"]["security"] = {"authentication": {"modes": ["SCRAM"]}}
        return mdb

    def services(self, member_cluster_name: Optional[str] = None) -> List[Optional[kubernetes.client.V1Service]]:
        """Returns a two element list with internal and external Services.

        Any of them might be None if the Service is not found.
        """
        services = []
        service_names = (
            self.svc_name(member_cluster_name),
            self.external_svc_name(member_cluster_name),
        )

        for name in service_names:
            try:
                svc = kubernetes.client.CoreV1Api(
                    api_client=get_member_cluster_api_client(member_cluster_name)
                ).read_namespaced_service(name, self.namespace)
                services.append(svc)
            except ApiException:
                services.append(None)

        return [services[0], services[1]]

    def read_statefulset(self, member_cluster_name: str = None) -> kubernetes.client.V1StatefulSet:
        if member_cluster_name is None:
            member_cluster_name = self.pick_one_om_member_cluster_name()

        return kubernetes.client.AppsV1Api(
            api_client=get_member_cluster_api_client(member_cluster_name)
        ).read_namespaced_stateful_set(self.om_sts_name(member_cluster_name), self.namespace)

    def pick_one_appdb_member_cluster_name(self) -> Optional[str]:
        if self.is_appdb_multi_cluster():
            return self.get_appdb_indexed_cluster_spec_items()[0][1]["clusterName"]
        else:
            return None

    def pick_one_om_member_cluster_name(self) -> Optional[str]:
        if self.is_om_multi_cluster():
            return self.get_om_indexed_cluster_spec_items()[0][1]["clusterName"]
        else:
            return None

    def read_appdb_statefulset(self, member_cluster_name: Optional[str] = None) -> kubernetes.client.V1StatefulSet:
        if member_cluster_name is None:
            member_cluster_name = self.pick_one_appdb_member_cluster_name()
        return kubernetes.client.AppsV1Api(
            api_client=get_member_cluster_api_client(member_cluster_name)
        ).read_namespaced_stateful_set(self.app_db_sts_name(member_cluster_name), self.namespace)

    def read_backup_statefulset(self, member_cluster_name: Optional[str] = None) -> kubernetes.client.V1StatefulSet:
        if member_cluster_name is None:
            member_cluster_name = self.pick_one_om_member_cluster_name()

        return kubernetes.client.AppsV1Api(
            api_client=get_member_cluster_api_client(member_cluster_name)
        ).read_namespaced_stateful_set(self.backup_daemon_sts_name(member_cluster_name), self.namespace)

    def read_om_pods(self) -> list[tuple[kubernetes.client.ApiClient, kubernetes.client.V1Pod]]:
        if self.is_om_multi_cluster():
            om_pod_names = self.get_om_pod_names_in_member_clusters()
            member_cluster_client_map = get_member_cluster_client_map()
            list_of_pods = []
            for cluster_name, om_pod_name in om_pod_names:
                member_cluster_client = member_cluster_client_map[cluster_name].api_client
                api_client = kubernetes.client.CoreV1Api(api_client=member_cluster_client)
                list_of_pods.append(
                    (
                        member_cluster_client,
                        api_client.read_namespaced_pod(om_pod_name, self.namespace),
                    )
                )
            return list_of_pods
        else:
            api_client = kubernetes.client.ApiClient()
            return [
                (
                    api_client,
                    kubernetes.client.CoreV1Api(api_client=api_client).read_namespaced_pod(podname, self.namespace),
                )
                for podname in get_pods(self.name + "-{}", self.get_total_number_of_om_replicas())
            ]

    def get_om_pod_names_in_member_clusters(self) -> list[tuple[str, str]]:
        """Returns list of tuples (cluster_name, pod_name) ordered by cluster index.
        Pod names are generated according to member count in spec.clusterSpecList.
        Clusters are ordered by cluster indexes in -cluster-mapping config map.
        """
        pod_names_per_cluster = []
        for cluster_idx, cluster_spec_item in self.get_om_indexed_cluster_spec_items():
            cluster_name = cluster_spec_item["clusterName"]
            if is_member_cluster(cluster_name):
                pod_names = multi_cluster_pod_names(self.name, [(cluster_idx, int(cluster_spec_item["members"]))])
            else:
                pod_names = [
                    self.om_pod_name(cluster_name, pod_idx)
                    for pod_idx in range(0, self.get_om_replicas_in_member_cluster(cluster_name))
                ]

            pod_names_per_cluster.extend([(cluster_name, pod_name) for pod_name in pod_names])

        return pod_names_per_cluster

    def get_om_cluster_spec_item(self, member_cluster_name: str) -> dict[str, any]:
        cluster_spec_items = [
            cluster_spec_item
            for idx, cluster_spec_item in self.get_om_indexed_cluster_spec_items()
            if cluster_spec_item["clusterName"] == member_cluster_name
        ]
        if len(cluster_spec_items) == 0:
            raise Exception(f"{member_cluster_name} not found on OM's cluster_spec_items")

        return cluster_spec_items[0]

    def get_om_sts_names_in_member_clusters(self) -> list[tuple[str, str]]:
        """Returns list of tuples (cluster_name, sts_name) ordered by cluster index.
        Statefulset names are generated according to member count in spec.clusterSpecList.
        Clusters are ordered by cluster indexes in -cluster-mapping config map.
        """
        sts_names_per_cluster = []
        for cluster_idx, cluster_spec_item in self.get_om_indexed_cluster_spec_items():
            cluster_name = cluster_spec_item["clusterName"]
            sts_names_per_cluster.append((cluster_name, self.om_sts_name(cluster_name)))

        return sts_names_per_cluster

    def get_appdb_sts_names_in_member_clusters(self) -> list[tuple[str, str]]:
        """Returns list of tuples (cluster_name, sts_name) ordered by cluster index.
        Statefulset names are generated according to member count in spec.applicationDatabase.clusterSpecList.
        Clusters are ordered by cluster indexes in -cluster-mapping config map.
        """
        sts_names_per_cluster = []
        for cluster_idx, cluster_spec_item in self.get_appdb_indexed_cluster_spec_items():
            cluster_name = cluster_spec_item["clusterName"]
            sts_names_per_cluster.append((cluster_name, self.app_db_sts_name(cluster_name)))

        return sts_names_per_cluster

    def get_backup_sts_names_in_member_clusters(self) -> list[tuple[str, str]]:
        """ """
        sts_names_per_cluster = []
        for cluster_idx, cluster_spec_item in self.get_om_indexed_cluster_spec_items():
            cluster_name = cluster_spec_item["clusterName"]
            sts_names_per_cluster.append((cluster_name, self.backup_daemon_sts_name(cluster_name)))

        return sts_names_per_cluster

    def get_om_member_cluster_names(self) -> list[str]:
        """Returns list of OpsManager member cluster names ordered by cluster index."""
        member_cluster_names = []
        for cluster_idx, cluster_spec_item in self.get_om_indexed_cluster_spec_items():
            member_cluster_names.append(cluster_spec_item["clusterName"])

        return member_cluster_names

    def get_appdb_member_cluster_names(self) -> list[str]:
        """Returns list of AppDB member cluster names ordered by cluster index."""
        member_cluster_names = []
        for cluster_idx, cluster_spec_item in self.get_appdb_indexed_cluster_spec_items():
            member_cluster_names.append(cluster_spec_item["clusterName"])

        return member_cluster_names

    def backup_daemon_pod_names(self, member_cluster_name: Optional[str] = None) -> list[tuple[str, str]]:
        """
        Returns list of tuples (cluster_name, pod_name) ordered by cluster index.
        Pod names are generated according to member count in spec.clusterSpecList[i].backup.members
        """
        pod_names_per_cluster = []
        for _, cluster_spec_item in self.get_om_indexed_cluster_spec_items():
            cluster_name = cluster_spec_item["clusterName"]
            if member_cluster_name is not None and cluster_name != member_cluster_name:
                continue
            members_in_cluster = cluster_spec_item.get("backup", {}).get(
                "members", self.get_backup_members_count(member_cluster_name=cluster_name)
            )
            pod_names = [
                f"{self.backup_daemon_sts_name(member_cluster_name=cluster_name)}-{idx}"
                for idx in range(int(members_in_cluster))
            ]

            pod_names_per_cluster.extend([(cluster_name, pod_name) for pod_name in pod_names])

        return pod_names_per_cluster

    def get_appdb_pod_names_in_member_clusters(self) -> list[tuple[str, str]]:
        """Returns list of tuples (cluster_name, pod_name) ordered by cluster index.
        Pod names are generated according to member count in spec.applicationDatabase.clusterSpecList.
        Clusters are ordered by cluster indexes in -cluster-mapping config map.
        """
        pod_names_per_cluster = []
        for (
            cluster_index,
            cluster_spec_item,
        ) in self.get_appdb_indexed_cluster_spec_items():
            cluster_name = cluster_spec_item["clusterName"]
            pod_names = multi_cluster_pod_names(
                self.app_db_name(), [(cluster_index, int(cluster_spec_item["members"]))]
            )
            pod_names_per_cluster.extend([(cluster_name, pod_name) for pod_name in pod_names])

        return pod_names_per_cluster

    def get_appdb_process_hostnames_in_member_clusters(self) -> list[tuple[str, str]]:
        """Returns list of tuples (cluster_name, service name) ordered by cluster index.
        Service names are generated according to member count in spec.applicationDatabase.clusterSpecList.
        Clusters are ordered by cluster indexes in -cluster-mapping config map.
        """
        service_names_per_cluster = []
        for (
            cluster_index,
            cluster_spec_item,
        ) in self.get_appdb_indexed_cluster_spec_items():
            cluster_name = cluster_spec_item["clusterName"]
            service_names = multi_cluster_service_names(
                self.app_db_name(), [(cluster_index, int(cluster_spec_item["members"]))]
            )
            service_names_per_cluster.extend([(cluster_name, service_name) for service_name in service_names])

        return service_names_per_cluster

    def get_appdb_hostnames_for_monitoring(self) -> list[str]:
        """
        Returns list of hostnames for appdb members.
        In case of multicluster appdb, hostnames are generated from the pod service fqdn.
        In case of single cluster, the hostnames use the headless service.
        """
        hostnames = []
        appdb_resource = self.get_appdb_resource()
        external_domain = appdb_resource["spec"].get("externalAccess", {}).get("externalDomain")
        if self.is_appdb_multi_cluster():
            for cluster_index, cluster_spec_item in self.get_appdb_indexed_cluster_spec_items():
                pod_names = multi_cluster_pod_names(
                    self.app_db_name(), [(cluster_index, int(cluster_spec_item["members"]))]
                )

                cluster_external_domain = cluster_spec_item.get("externalAccess", {}).get(
                    "externalDomain", external_domain
                )
                if cluster_external_domain is not None:
                    hostnames.extend([f"{pod_name}.{cluster_external_domain}" for pod_name in pod_names])
                else:
                    hostnames.extend([f"{pod_name}-svc.{self.namespace}.svc.cluster.local" for pod_name in pod_names])
        else:
            resource_name = appdb_resource["metadata"]["name"]
            service_name = f"{resource_name}-svc"
            namespace = appdb_resource["metadata"]["namespace"]

            for index in range(appdb_resource["spec"]["members"]):
                if external_domain is not None:
                    hostnames.append(f"{resource_name}-{index}.{external_domain}")
                else:
                    hostnames.append(f"{resource_name}-{index}.{service_name}.{namespace}.svc.cluster.local")

        return hostnames

    def get_appdb_indexed_cluster_spec_items(self) -> list[tuple[int, dict[str, any]]]:
        """Returns ordered list (by cluster index) of tuples (cluster index, clusterSpecItem) from spec.applicationDatabase.clusterSpecList.
        Cluster indexes are read from -cluster-mapping config map.
        """
        if not self.is_appdb_multi_cluster():
            return self.get_legacy_central_cluster(self.get_appdb_members_count())

        cluster_index_mapping = read_deployment_state(self.app_db_name(), self.namespace, get_central_cluster_client())[
            "clusterMapping"
        ]
        result = []
        for cluster_spec_item in self["spec"]["applicationDatabase"].get("clusterSpecList", []):
            result.append(
                (
                    int(cluster_index_mapping[cluster_spec_item["clusterName"]]),
                    cluster_spec_item,
                )
            )

        return sorted(result, key=lambda x: x[0])

    def get_om_indexed_cluster_spec_items(self) -> list[tuple[int, dict[str, str]]]:
        """Returns an ordered list (by cluster index) of tuples (cluster index, clusterSpecItem) from spec.clusterSpecList.
        Cluster indexes are read from -cluster-mapping config map.
        """
        if not self.is_om_multi_cluster():
            return self.get_legacy_central_cluster(self.get_total_number_of_om_replicas())

        cluster_mapping = read_deployment_state(self.name, self.namespace, get_central_cluster_client())[
            "clusterMapping"
        ]
        result = [
            (
                int(cluster_mapping[cluster_spec_item["clusterName"]]),
                cluster_spec_item,
            )
            for cluster_spec_item in self["spec"].get("clusterSpecList", [])
        ]
        return sorted(result, key=lambda x: x[0])

    @staticmethod
    def get_legacy_central_cluster(replicas: int) -> list[tuple[int, dict[str, str]]]:
        return [(0, {"clusterName": LEGACY_CENTRAL_CLUSTER_NAME, "members": str(replicas)})]

    def read_appdb_pods(self) -> list[tuple[kubernetes.client.ApiClient, kubernetes.client.V1Pod]]:
        """Returns list of tuples[api_client used, pod]."""
        if self.is_appdb_multi_cluster():
            appdb_pod_names = self.get_appdb_pod_names_in_member_clusters()
            member_cluster_client_map = get_member_cluster_client_map()
            list_of_pods = []
            for cluster_name, appdb_pod_name in appdb_pod_names:
                member_cluster_client = member_cluster_client_map[cluster_name].api_client
                api_client = kubernetes.client.CoreV1Api(api_client=member_cluster_client)
                list_of_pods.append(
                    (
                        member_cluster_client,
                        api_client.read_namespaced_pod(appdb_pod_name, self.namespace),
                    )
                )

            return list_of_pods
        else:
            api_client = kubernetes.client.ApiClient()
            return [
                (
                    api_client,
                    kubernetes.client.CoreV1Api(api_client=api_client).read_namespaced_pod(pod_name, self.namespace),
                )
                for pod_name in get_pods(self.app_db_name() + "-{}", self.get_appdb_members_count())
            ]

    def read_backup_pods(self) -> list[tuple[kubernetes.client.ApiClient, kubernetes.client.V1Pod]]:
        if self.is_om_multi_cluster():
            backup_pod_names = self.backup_daemon_pod_names()
            member_cluster_client_map = get_member_cluster_client_map()
            list_of_pods = []
            for cluster_name, backup_pod_name in backup_pod_names:
                member_cluster_client = member_cluster_client_map[cluster_name].api_client
                api_client = kubernetes.client.CoreV1Api(api_client=member_cluster_client)
                list_of_pods.append(
                    (
                        member_cluster_client,
                        api_client.read_namespaced_pod(backup_pod_name, self.namespace),
                    )
                )
            return list_of_pods
        else:
            api_client = kubernetes.client.ApiClient()
            return [
                (
                    api_client,
                    kubernetes.client.CoreV1Api().read_namespaced_pod(pod_name, self.namespace),
                )
                for pod_name in get_pods(
                    self.backup_daemon_sts_name() + "-{}",
                    self.get_backup_members_count(member_cluster_name=LEGACY_CENTRAL_CLUSTER_NAME),
                )
            ]

    @staticmethod
    def get_backup_daemon_container_status(
        backup_daemon_pod: kubernetes.client.V1Pod,
    ) -> kubernetes.client.V1ContainerStatus:
        return next(filter(lambda c: c.name == "mongodb-backup-daemon", backup_daemon_pod.status.container_statuses))

    TRACER.start_as_current_span("wait_until_backup_pods_become_ready")

    def wait_until_backup_pods_become_ready(self, timeout=300):
        def backup_daemons_are_ready():
            try:
                for _, backup_pod in self.read_backup_pods():
                    if not MongoDBOpsManager.get_backup_daemon_container_status(backup_pod).ready:
                        return False
                return True
            except Exception as e:
                print("Error checking if pod is ready: " + str(e))
                return False

        KubernetesTester.wait_until(backup_daemons_are_ready, timeout=timeout)

    def read_gen_key_secret(self, member_cluster_name: Optional[str] = None) -> kubernetes.client.V1Secret:
        return kubernetes.client.CoreV1Api(get_member_cluster_api_client(member_cluster_name)).read_namespaced_secret(
            self.name + "-gen-key", self.namespace
        )

    def read_api_key_secret(self, namespace=None) -> kubernetes.client.V1Secret:
        """Reads the API key secret for the global admin created by the Operator. Note, that the secret is
        located in the Operator namespace - not Ops Manager one, so the 'namespace' parameter must be passed
        if the Ops Manager is installed in a separate namespace"""
        if namespace is None:
            namespace = self.namespace
        return kubernetes.client.CoreV1Api().read_namespaced_secret(self.api_key_secret(namespace), namespace)

    def read_appdb_generated_password_secret(self) -> kubernetes.client.V1Secret:
        return kubernetes.client.CoreV1Api().read_namespaced_secret(self.app_db_name() + "-om-password", self.namespace)

    def read_appdb_generated_password(self) -> str:
        data = self.read_appdb_generated_password_secret().data
        return KubernetesTester.decode_secret(data)["password"]

    def read_appdb_agent_password_secret(self) -> kubernetes.client.V1Secret:
        return kubernetes.client.CoreV1Api().read_namespaced_secret(
            self.app_db_name() + "-agent-password", self.namespace
        )

    def read_appdb_agent_keyfile_secret(self) -> kubernetes.client.V1Secret:
        return kubernetes.client.CoreV1Api().read_namespaced_secret(self.app_db_name() + "-keyfile", self.namespace)

    def read_appdb_connection_url(self) -> str:
        secret = kubernetes.client.CoreV1Api().read_namespaced_secret(
            self.get_appdb_connection_url_secret_name(), self.namespace
        )
        return KubernetesTester.decode_secret(secret.data)["connectionString"]

    def read_appdb_members_from_connection_url_secret(self) -> str:
        return re.findall(r"[@,]([^@,\/]+)", self.read_appdb_connection_url())

    def create_admin_secret(
        self,
        user_name="jane.doe@example.com",
        password="Passw0rd.",
        first_name="Jane",
        last_name="Doe",
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ):
        data = {
            "Username": user_name,
            "Password": password,
            "FirstName": first_name,
            "LastName": last_name,
        }
        create_or_update_secret(self.namespace, self.get_admin_secret_name(), data, api_client=api_client)

    def get_automation_config_tester(self) -> AutomationConfigTester:
        api_client = None
        if self.is_appdb_multi_cluster():
            cluster_name = self.pick_one_appdb_member_cluster_name()
            api_client = get_member_cluster_client_map()[cluster_name].api_client

        secret = (
            kubernetes.client.CoreV1Api(api_client=api_client)
            .read_namespaced_secret(self.app_db_name() + "-config", self.namespace)
            .data
        )
        automation_config_str = b64decode(secret["cluster-config.json"]).decode("utf-8")
        return AutomationConfigTester(json.loads(automation_config_str))

    def get_or_create_mongodb_connection_config_map(
        self,
        mongodb_name: str,
        project_name: str,
        namespace=None,
        ca_config_map_name: Optional[str] = None,
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ) -> str:
        """Creates the configmap containing the information needed to connect to OM"""
        config_map_name = f"{mongodb_name}-project-config"

        # the namespace can be different from OM one if the MongoDB is created in a separate namespace
        if namespace is None:
            namespace = self.namespace

        data = {
            "baseUrl": self.om_status().get_url(),
            "projectName": f"{namespace}-{project_name}",
            "orgId": "",
        }
        if ca_config_map_name is not None:
            data["sslMMSCAConfigMap"] = ca_config_map_name

        create_or_update_configmap(namespace, config_map_name, data, api_client=api_client)

        return config_map_name

    def get_om_tester(
        self,
        project_name: Optional[str] = None,
        base_url: Optional[str] = None,
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ) -> OMTester:
        """Returns the instance of OMTester helping to check the state of Ops Manager deployed in Kubernetes."""
        agent_api_key = self.agent_api_key(api_client)
        api_key_secret = read_secret(
            KubernetesTester.get_namespace(),
            self.api_key_secret(KubernetesTester.get_namespace(), api_client=api_client),
            api_client=api_client,
        )

        # Check if it's an old stile secret or a new one
        if "publicApiKey" in api_key_secret:
            om_context = OMContext(
                self.om_status().get_url() if not base_url else base_url,
                api_key_secret["user"],
                api_key_secret["publicApiKey"],
                project_name=project_name,
                agent_api_key=agent_api_key,
            )
        else:
            om_context = OMContext(
                self.om_status().get_url() if not base_url else base_url,
                api_key_secret["publicKey"],
                api_key_secret["privateKey"],
                project_name=project_name,
                agent_api_key=agent_api_key,
            )
        return OMTester(om_context)

    def get_appdb_service_names_in_multi_cluster(self) -> list[str]:
        cluster_indexes_with_members = self.get_appdb_member_cluster_indexes_with_member_count()
        for _, cluster_spec_item in self.get_appdb_indexed_cluster_spec_items():
            return multi_cluster_service_names(self.app_db_name(), cluster_indexes_with_members)

    def get_appdb_member_cluster_indexes_with_member_count(self) -> list[tuple[int, int]]:
        return [
            (cluster_index, int(cluster_spec_item["members"]))
            for cluster_index, cluster_spec_item in self.get_appdb_indexed_cluster_spec_items()
        ]

    def get_appdb_tester(self, **kwargs) -> MongoTester:
        if self.is_appdb_multi_cluster():
            return MultiReplicaSetTester(
                service_names=self.get_appdb_service_names_in_multi_cluster(),
                port="27017",
                namespace=self.namespace,
                **kwargs,
            )
        else:
            return ReplicaSetTester(
                self.app_db_name(),
                replicas_count=self.appdb_status().get_members(),
                **kwargs,
            )

    def pod_urls(self):
        """Returns http urls to each pod in the Ops Manager"""
        return [
            "http://{}".format(host)
            for host in build_list_of_hosts(
                self.name, self.namespace, self.get_total_number_of_om_replicas(), port=8080
            )
        ]

    def set_version(self, version: Optional[str]):
        """Sets a specific `version` if set. If `version` is None, then skip."""
        if version is not None:
            self["spec"]["version"] = version
        return self

    def update_key_to_programmatic(self):
        """
        Attempts to create a Programmatic API Key to be used after updating to
        newer OM5, which don't support old-style API Key.
        """

        url = self.om_status().get_url()
        whitelist_endpoint = f"{url}/api/public/v1.0/admin/whitelist"
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        whitelist_entries = [
            {"cidrBlock": "0.0.0.0/1", "description": "first block"},
            {"cidrBlock": "128.0.0.0/1", "description": "second block"},
        ]

        secret_name = self.api_key_secret(self.namespace)
        current_creds = read_secret(self.namespace, secret_name)
        user = current_creds["user"]
        password = current_creds["publicApiKey"]
        auth = HTTPDigestAuth(user, password)

        for entry in whitelist_entries:
            response = requests.post(whitelist_endpoint, json=entry, headers=headers, auth=auth)
            assert response.status_code == 200

        data = {
            "desc": "Creating a programmatic API key before updating to 5.0.0",
            "roles": ["GLOBAL_OWNER"],
        }

        endpoint = f"{url}/api/public/v1.0/admin/apiKeys"
        response = requests.post(endpoint, json=data, headers=headers, auth=auth)
        response_data = response.json()
        if "privateKey" not in response_data:
            assert response_data == {}

        new_creds = {
            "publicApiKey": response_data["privateKey"],
            "user": response_data["publicKey"],
        }

        KubernetesTester.update_secret(self.namespace, secret_name, new_creds)

    def allow_mdb_rc_versions(self):
        """
        Sets configurations parameters for OM to be able to download RC versions.
        """

        if "configuration" not in self["spec"]:
            self["spec"]["configuration"] = {}

        self["spec"]["configuration"]["mms.featureFlag.automation.mongoDevelopmentVersions"] = "enabled"
        self["spec"]["configuration"]["mongodb.release.autoDownload.rc"] = "true"
        self["spec"]["configuration"]["mongodb.release.autoDownload.development"] = "true"

    def set_appdb_version(self, version: str):
        self["spec"]["applicationDatabase"]["version"] = version

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDBOpsManager| status:".format(self.get_status())

    def get_appdb_members_count(self) -> int:
        if self.is_appdb_multi_cluster():
            return sum(i[1] for i in self.get_appdb_member_cluster_indexes_with_member_count())
        return self["spec"]["applicationDatabase"]["members"]

    def get_appdb_connection_url_secret_name(self):
        return f"{self.app_db_name()}-connection-string"

    def get_total_number_of_om_replicas(self) -> int:
        if not self.is_om_multi_cluster():
            return self["spec"]["replicas"]

        return sum([item["members"] for _, item in self.get_om_indexed_cluster_spec_items()])

    def get_om_replicas_in_member_cluster(self, member_cluster_name: Optional[str] = None) -> int:
        if is_member_cluster(member_cluster_name):
            return self.get_om_cluster_spec_item(member_cluster_name)["members"]

        return self["spec"]["replicas"]

    def get_backup_members_count(self, member_cluster_name: Optional[str] = None) -> int:
        if not self["spec"].get("backup", {}).get("enabled", False):
            return 0

        if is_member_cluster(member_cluster_name):
            cluster_spec_item = self.get_om_cluster_spec_item(member_cluster_name)
            members = cluster_spec_item.get("backup", {}).get("members", None)
            if members is not None:
                return members

        return self["spec"]["backup"].get("members", 0)

    def get_admin_secret_name(self) -> str:
        return self["spec"]["adminCredentials"]

    def get_version(self) -> str:
        return self["spec"]["version"]

    def get_status(self) -> Optional[str]:
        if "status" not in self:
            return None
        return self["status"]

    def api_key_secret(self, namespace: str, api_client: Optional[kubernetes.client.ApiClient] = None) -> str:
        old_secret_name = self.name + "-admin-key"

        # try to read the old secret, if it's present return it, else return the new secret name
        try:
            kubernetes.client.CoreV1Api(api_client=api_client).read_namespaced_secret(old_secret_name, namespace)
        except ApiException as e:
            if e.status == 404:
                return "{}-{}-admin-key".format(self.namespace, self.name)

        return old_secret_name

    def agent_api_key(self, api_client: Optional[kubernetes.client.ApiClient] = None) -> str:
        secret_name = None
        member_cluster = self.pick_one_appdb_member_cluster_name()
        appdb_sts = self.read_appdb_statefulset(member_cluster_name=member_cluster)

        for volume in appdb_sts.spec.template.spec.volumes:
            if volume.name == "agent-api-key":
                secret_name = volume.secret.secret_name
                break

        if secret_name == None:
            return None

        return read_secret(self.namespace, secret_name, get_member_cluster_api_client(member_cluster))["agentApiKey"]

    def om_sts_name(self, member_cluster_name: Optional[str] = None) -> str:
        if is_member_cluster(member_cluster_name):
            cluster_idx = self.get_om_member_cluster_index(member_cluster_name)
            return f"{self.name}-{cluster_idx}"
        else:
            return self.name

    def om_pod_name(self, member_cluster_name: str, pod_idx: int) -> str:
        if is_member_cluster(member_cluster_name):
            cluster_idx = self.get_om_member_cluster_index(member_cluster_name)
            return f"{self.name}-{cluster_idx}-{pod_idx}"
        else:
            return f"{self.name}-{pod_idx}"

    def app_db_name(self) -> str:
        return self.name + "-db"

    def app_db_sts_name(self, member_cluster_name: Optional[str] = None) -> str:
        if is_member_cluster(member_cluster_name):
            cluster_idx = self.get_appdb_member_cluster_index(member_cluster_name)
            return f"{self.name}-db-{cluster_idx}"
        else:
            return self.name + "-db"

    def get_om_member_cluster_index(self, member_cluster_name: str) -> int:
        for cluster_idx, cluster_spec_item in self.get_om_indexed_cluster_spec_items():
            if cluster_spec_item["clusterName"] == member_cluster_name:
                return cluster_idx
        raise Exception(f"member cluster {member_cluster_name} not found in OM cluster spec items")

    def get_appdb_member_cluster_index(self, member_cluster_name: str) -> int:
        for (
            cluster_idx,
            cluster_spec_item,
        ) in self.get_appdb_indexed_cluster_spec_items():
            if cluster_spec_item["clusterName"] == member_cluster_name:
                return cluster_idx

        raise Exception(f"member cluster {member_cluster_name} not found in AppDB cluster spec items")

    def app_db_password_secret_name(self) -> str:
        return self.app_db_name() + "-om-user-password"

    def backup_daemon_sts_name(self, member_cluster_name: Optional[str] = None) -> str:
        return self.om_sts_name(member_cluster_name) + "-backup-daemon"

    def backup_daemon_pods_headless_fqdns(self) -> list[str]:
        fqdns = []
        for member_cluster_name in self.get_om_member_cluster_names():
            member_fqdns = [
                f"{pod_name}.{self.backup_daemon_sts_name(member_cluster_name)}-svc.{self.namespace}.svc.cluster.local"
                for _, pod_name in self.backup_daemon_pod_names(member_cluster_name=member_cluster_name)
            ]
            fqdns.extend(member_fqdns)

        return fqdns

    def svc_name(self, member_cluster_name: Optional[str] = None) -> str:
        return self.name + "-svc"

    def external_svc_name(self, member_cluster_name: Optional[str] = None) -> str:
        return self.name + "-svc-ext"

    def download_mongodb_binaries(self, version: str):
        """Downloads mongodb binary in each OM pod, optional downloads MongoDB Tools"""
        distros = [
            f"mongodb-linux-x86_64-rhel80-{version}.tgz",
            f"mongodb-linux-x86_64-rhel8-{version}.tgz",
            f"mongodb-linux-x86_64-ubuntu1604-{version}.tgz",
            f"mongodb-linux-x86_64-ubuntu1804-{version}.tgz",
        ]

        for api_client, pod in self.read_om_pods():
            for distro in distros:
                cmd = [
                    "curl",
                    "-L",
                    f"https://fastdl.mongodb.org/linux/{distro}",
                    "-o",
                    f"/mongodb-ops-manager/mongodb-releases/{distro}",
                ]

                KubernetesTester.run_command_in_pod_container(
                    pod.metadata.name,
                    self.namespace,
                    cmd,
                    container="mongodb-ops-manager",
                    api_client=api_client,
                )

    def update_version_manifest(self):
        major_version = self.get_version()[:3]
        tester = self.get_om_tester()
        tester.api_update_version_manifest(major_version=major_version)

    def is_appdb_multi_cluster(self):
        return self["spec"].get("applicationDatabase", {}).get("topology", "") == "MultiCluster"

    def is_om_multi_cluster(self):
        return self["spec"].get("topology", "") == "MultiCluster"

    class StatusCommon:
        def assert_reaches_phase(
            self,
            phase: Phase,
            msg_regexp=None,
            timeout=None,
            ignore_errors=False,
        ):
            intermediate_events = (
                # This can be an intermediate error, right before we check for this secret we create it.
                # The cluster might just be slow
                "failed to locate the api key secret",
                # etcd might be slow
                "etcdserver: request timed out",
            )

            start_time = time.time()
            self.ops_manager.wait_for(
                lambda s: in_desired_state(
                    current_state=self.get_phase(),
                    desired_state=phase,
                    current_generation=self.ops_manager.get_generation(),
                    observed_generation=self.get_observed_generation(),
                    current_message=self.get_message(),
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
            return self.ops_manager.wait_for(lambda s: self.get_phase() != phase, timeout, should_raise=True)

        def assert_status_resource_not_ready(self, name: str, kind: str = "StatefulSet", msg_regexp=None, idx=0):
            """Checks the element in 'resources_not_ready' field by index 'idx'"""
            assert self.get_resources_not_ready()[idx]["kind"] == kind
            assert self.get_resources_not_ready()[idx]["name"] == name
            assert re.search(msg_regexp, self.get_resources_not_ready()[idx]["message"]) is not None

        def assert_empty_status_resources_not_ready(self):
            assert self.get_resources_not_ready() is None

    class BackupStatus(StatusCommon):
        def __init__(self, ops_manager: MongoDBOpsManager):
            self.ops_manager = ops_manager

        def assert_abandons_phase(self, phase: Phase, timeout=400):
            super().assert_abandons_phase(phase, timeout)

        def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=800, ignore_errors=True):
            super().assert_reaches_phase(phase, msg_regexp, timeout, ignore_errors)

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

    class AppDbStatus(StatusCommon):
        def __init__(self, ops_manager: MongoDBOpsManager):
            self.ops_manager = ops_manager

        def assert_abandons_phase(self, phase: Phase, timeout=400):
            super().assert_abandons_phase(phase, timeout)

        def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=1000, ignore_errors=False):
            super().assert_reaches_phase(phase, msg_regexp, timeout, ignore_errors)

        def get_phase(self) -> Optional[Phase]:
            try:
                return Phase[self.ops_manager.get_status()["applicationDatabase"]["phase"]]
            except (KeyError, TypeError):
                return None

        def get_message(self) -> Optional[str]:
            try:
                return self.ops_manager.get_status()["applicationDatabase"]["message"]
            except (KeyError, TypeError):
                return None

        def get_observed_generation(self) -> Optional[int]:
            try:
                return self.ops_manager.get_status()["applicationDatabase"]["observedGeneration"]
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
                return self.ops_manager.get_status()["applicationDatabase"]["resourcesNotReady"]
            except (KeyError, TypeError):
                return None

    class OmStatus(StatusCommon):
        def __init__(self, ops_manager: MongoDBOpsManager):
            self.ops_manager = ops_manager

        def assert_abandons_phase(self, phase: Phase, timeout=400):
            super().assert_abandons_phase(phase, timeout)

        def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=1200, ignore_errors=False):
            super().assert_reaches_phase(phase, msg_regexp, timeout, ignore_errors)

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
