import time
from typing import Dict

import pytest
from kubernetes import client
from kubetester import (
    assert_pod_container_security_context,
    assert_pod_security_context,
    try_load,
)
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_default_architecture_static, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ReplicaSetTester
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import (
    DATABASE_SA_NAME,
    LEGACY_OPERATOR_NAME,
    OPERATOR_NAME,
    assert_log_rotation_backup_monitoring,
    assert_log_rotation_process,
    setup_log_rotate_for_agents,
)

DEFAULT_BACKUP_VERSION = "11.12.0.7388-1"
DEFAULT_MONITORING_AGENT_VERSION = "11.12.0.7388-1"
RESOURCE_NAME = "my-replica-set"


def _get_group_id(envs) -> str:
    for e in envs:
        if e.name == "GROUP_ID":
            return e.value
    return ""


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str, cluster_domain: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), "my-replica-set", namespace)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["clusterDomain"] = cluster_domain

    # Setting podSpec shortcut values here to test they are still
    # added as resources when needed.
    if is_default_architecture_static():
        resource["spec"]["podSpec"] = {
            "podTemplate": {
                "spec": {
                    "containers": [
                        {
                            "name": "mongodb-agent",
                            "resources": {
                                "limits": {
                                    "cpu": "1",
                                    "memory": "1Gi",
                                },
                                "requests": {"cpu": "0.2", "memory": "300M"},
                            },
                        }
                    ]
                }
            }
        }
    else:
        resource["spec"]["podSpec"] = {
            "podTemplate": {
                "spec": {
                    "containers": [
                        {
                            "name": "mongodb-enterprise-database",
                            "resources": {
                                "limits": {
                                    "cpu": "1",
                                    "memory": "1Gi",
                                },
                                "requests": {"cpu": "0.2", "memory": "300M"},
                            },
                        }
                    ]
                }
            }
        }

    setup_log_rotate_for_agents(resource)
    resource.update()

    return resource


@pytest.fixture(scope="class")
def config_version():
    class ConfigVersion:
        def __init__(self):
            self.version = 0

    return ConfigVersion()


@pytest.mark.e2e_replica_set
class TestReplicaSetCreation(KubernetesTester):
    def test_initialize_config_version(self, config_version):
        self.ensure_group(self.get_om_org_id(), self.namespace)
        config = self.get_automation_config()
        config_version.version = config["version"]

    def test_mdb_created(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_replica_set_sts_exists(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)
        assert sts

    @pytest.mark.flaky(reruns=15, reruns_delay=5)
    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 3
        assert sts.status.ready_replicas == 3

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)

        assert sts.metadata.name == RESOURCE_NAME
        assert sts.metadata.labels["app"] == "my-replica-set-svc"
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == "mongodb.com/v1"
        assert owner_ref0.kind == "MongoDB"
        assert owner_ref0.name == RESOURCE_NAME

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)
        assert sts.spec.replicas == 3

    def test_sts_template(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)

        tmpl = sts.spec.template
        assert tmpl.metadata.labels["app"] == "my-replica-set-svc"
        assert tmpl.metadata.labels["controller"] == LEGACY_OPERATOR_NAME
        assert tmpl.spec.service_account_name == DATABASE_SA_NAME
        assert tmpl.spec.affinity.node_affinity is None
        assert tmpl.spec.affinity.pod_affinity is None
        assert tmpl.spec.affinity.pod_anti_affinity is not None

    def _get_pods(self, podname, qty=3):
        return [podname.format(i) for i in range(qty)]

    def test_replica_set_pods_exists(self):
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.metadata.name == podname

    def test_pods_are_running(self):
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.status.phase == "Running"

    def test_pods_containers(self):
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            if is_default_architecture_static():
                assert c0.name == "mongodb-agent"
            else:
                assert c0.name == "mongodb-enterprise-database"

    def test_pods_containers_ports(self):
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            assert c0.ports[0].container_port == 27017
            assert c0.ports[0].host_ip is None
            assert c0.ports[0].host_port is None
            assert c0.ports[0].protocol == "TCP"

    def test_pods_resources(self):
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            assert c0.resources.limits["cpu"] == "1"
            assert c0.resources.limits["memory"] == "1Gi"
            assert c0.resources.requests["cpu"] == "200m"
            assert c0.resources.requests["memory"] == "300M"

    def test_pods_container_envvars(self):
        for pod_name in self._get_pods("my-replica-set-{}", 3):
            assert_container_env_vars(self.namespace, pod_name)

    def test_service_is_created(self):
        svc = self.corev1.read_namespaced_service("my-replica-set-svc", self.namespace)
        assert svc

    def test_clusterip_service_exists(self):
        """Test that replica set is not exposed externally."""
        services = self.clients("corev1").list_namespaced_service(
            self.get_namespace(),
            label_selector=f"controller={LEGACY_OPERATOR_NAME}",
        )

        # 1 for replica set
        assert len(services.items) == 1
        assert services.items[0].spec.type == "ClusterIP"

    def test_security_context_pods(self, operator_installation_config: Dict[str, str]):

        managed = operator_installation_config["managedSecurityContext"] == "true"
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert_pod_security_context(pod, managed)
            assert_pod_container_security_context(pod.spec.containers[0], managed)

    @skip_if_local
    def test_security_context_operator(self, operator_installation_config: Dict[str, str]):
        # todo there should be a better way to find the pods for deployment
        response = self.corev1.list_namespaced_pod(self.namespace)
        operator_pod = [pod for pod in response.items if pod.metadata.name.startswith(f"{OPERATOR_NAME}-")][0]
        security_context = operator_pod.spec.security_context
        if operator_installation_config["managedSecurityContext"] == "false":
            assert security_context.run_as_user == 2000
            assert security_context.run_as_non_root
            assert security_context.fs_group is None
        else:
            # Note, that this code is a bit fragile as may depend on the version of Openshift, but we need to verify
            # that this is not "our" security context but the one generated by Openshift
            assert security_context.run_as_user is None
            assert security_context.run_as_non_root is None
            assert security_context.se_linux_options is not None
            assert security_context.fs_group is not None
            assert security_context.fs_group != 2000

    def test_om_processes_are_created(self):
        config = self.get_automation_config()
        assert len(config["processes"]) == 3

    def test_om_replica_set_is_created(self):
        config = self.get_automation_config()
        assert len(config["replicaSets"]) == 1

    def test_om_processes(self, custom_mdb_version: str, cluster_domain: str):
        config = self.get_automation_config()
        processes = config["processes"]

        for idx in range(0, 2):
            name = f"my-replica-set-{idx}"
            p = processes[idx]
            assert p["name"] == name
            assert p["processType"] == "mongod"
            assert custom_mdb_version in p["version"]
            assert p["authSchemaVersion"] == 5
            assert p["featureCompatibilityVersion"] == fcv_from_version(custom_mdb_version)
            assert p["hostname"] == "{}.my-replica-set-svc.{}.svc.{}".format(name, self.namespace, cluster_domain)
            assert p["args2_6"]["net"]["port"] == 27017
            assert p["args2_6"]["replication"]["replSetName"] == RESOURCE_NAME
            assert p["args2_6"]["storage"]["dbPath"] == "/data"
            assert p["args2_6"]["systemLog"]["destination"] == "file"
            assert p["args2_6"]["systemLog"]["path"] == "/var/log/mongodb-mms-automation/mongodb.log"
            assert_log_rotation_process(p)

    def test_om_replica_set(self):
        config = self.get_automation_config()
        rs = config["replicaSets"]
        assert rs[0]["_id"] == RESOURCE_NAME

        for idx in range(0, 2):
            m = rs[0]["members"][idx]
            assert m["_id"] == idx
            assert m["arbiterOnly"] is False
            assert m["hidden"] is False
            assert m["buildIndexes"] is True
            assert m["host"] == f"my-replica-set-{idx}"
            assert m["votes"] == 1
            assert m["priority"] == 1.0

    def test_monitoring_versions(self, cluster_domain: str):
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 3

        # Monitoring agent is installed on all hosts
        for i in range(0, 3):
            # baseUrl is not present in Cloud Manager response
            if "baseUrl" in mv[i]:
                assert mv[i]["baseUrl"] is None
            hostname = "my-replica-set-{}.my-replica-set-svc.{}.svc.{}".format(i, self.namespace, cluster_domain)
            assert mv[i]["hostname"] == hostname
            assert mv[i]["name"] == DEFAULT_MONITORING_AGENT_VERSION

    def test_monitoring_log_rotation(self, cluster_domain: str):
        mv = self.get_monitoring_config()
        assert_log_rotation_backup_monitoring(mv)

    def test_backup(self, cluster_domain):
        config = self.get_automation_config()
        # 1 backup agent per host
        bkp = config["backupVersions"]
        assert len(bkp) == 3

        # Backup agent is installed on all hosts
        for i in range(0, 3):
            hostname = "my-replica-set-{}.my-replica-set-svc.{}.svc.{}".format(i, self.namespace, cluster_domain)
            assert bkp[i]["hostname"] == hostname
            assert bkp[i]["name"] == DEFAULT_BACKUP_VERSION

    def test_backup_log_rotation(self):
        bvk = self.get_backup_config()
        assert_log_rotation_backup_monitoring(bvk)

    def test_proper_automation_config_version(self, config_version):
        config = self.get_automation_config()
        # We create 3 members of the replicaset here, so there will be 2 changes.
        # Anything more than 2 + 4 (logRotation has 4 changes) changes
        # indicates that we're sending more things to the Ops/Cloud Manager than we should.
        if is_default_architecture_static():
            assert (config["version"] - config_version.version) == 5
        else:
            assert (config["version"] - config_version.version) == 6

    @skip_if_local
    def test_replica_set_was_configured(self, cluster_domain: str):
        ReplicaSetTester(RESOURCE_NAME, 3, ssl=False, cluster_domain=cluster_domain).assert_connectivity()

    def test_replica_set_was_configured_with_srv(self, cluster_domain: str):
        ReplicaSetTester(RESOURCE_NAME, 3, ssl=False, srv=True, cluster_domain=cluster_domain).assert_connectivity()


@pytest.mark.e2e_replica_set
def test_replica_set_can_be_scaled_to_single_member(replica_set: MongoDB):
    """Scaling to 1 member somehow changes the way the Replica Set is represented and there
    will be no more a "Primary" or "Secondaries" in the client, so the test does not check
    Replica Set state. An additional test `test_replica_set_can_be_scaled_down_and_connectable`
    scales down to 3 (from 5) and makes sure the Replica is connectable with "Primary" and
    "Secondaries" set."""
    replica_set["spec"]["members"] = 1
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=1200)

    actester = AutomationConfigTester(KubernetesTester.get_automation_config())

    # we should have only 1 process on the replica-set
    assert len(actester.get_replica_set_processes(replica_set.name)) == 1

    assert replica_set["status"]["members"] == 1

    replica_set.assert_connectivity()


@pytest.mark.e2e_replica_set
class TestReplicaSetScaleUp(KubernetesTester):
    def test_mdb_updated(self, replica_set: MongoDB):
        replica_set["spec"]["members"] = 5
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=500)

    def test_replica_set_sts_should_exist(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)
        assert sts

    def test_sts_update(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 5
        assert sts.status.ready_replicas == 5

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)

        assert sts.metadata.name == RESOURCE_NAME
        assert sts.metadata.labels["app"] == "my-replica-set-svc"
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == "mongodb.com/v1"
        assert owner_ref0.kind == "MongoDB"
        assert owner_ref0.name == RESOURCE_NAME

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)
        assert sts.spec.replicas == 5

    def _get_pods(self, podname, qty):
        return [podname.format(i) for i in range(qty)]

    def test_replica_set_pods_exists(self):
        for podname in self._get_pods("my-replica-set-{}", 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.metadata.name == podname

    def test_pods_are_running(self):
        for podname in self._get_pods("my-replica-set-{}", 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.status.phase == "Running"

    def test_pods_containers(self):
        for podname in self._get_pods("my-replica-set-{}", 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            if is_default_architecture_static():
                assert c0.name == "mongodb-agent"
            else:
                assert c0.name == "mongodb-enterprise-database"

    def test_pods_containers_ports(self):
        for podname in self._get_pods("my-replica-set-{}", 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            assert c0.ports[0].container_port == 27017
            assert c0.ports[0].host_ip is None
            assert c0.ports[0].host_port is None
            assert c0.ports[0].protocol == "TCP"

    def test_pods_container_envvars(self):
        for pod_name in self._get_pods("my-replica-set-{}", 5):
            assert_container_env_vars(self.namespace, pod_name)

    def test_service_is_created(self):
        svc = self.corev1.read_namespaced_service("my-replica-set-svc", self.namespace)
        assert svc

    def test_om_processes_are_created(self):
        config = self.get_automation_config()
        assert len(config["processes"]) == 5

    def test_om_replica_set_is_created(self):
        config = self.get_automation_config()
        assert len(config["replicaSets"]) == 1

    def test_om_processes(self, custom_mdb_version: str, cluster_domain: str):
        config = self.get_automation_config()
        processes = config["processes"]
        for idx in range(0, 4):
            name = f"my-replica-set-{idx}"
            p = processes[idx]
            assert p["name"] == name
            assert p["processType"] == "mongod"
            assert custom_mdb_version in p["version"]
            assert p["authSchemaVersion"] == 5
            assert p["featureCompatibilityVersion"] == fcv_from_version(custom_mdb_version)
            assert p["hostname"] == "{}.my-replica-set-svc.{}.svc.{}".format(name, self.namespace, cluster_domain)
            assert p["args2_6"]["net"]["port"] == 27017
            assert p["args2_6"]["replication"]["replSetName"] == RESOURCE_NAME
            assert p["args2_6"]["storage"]["dbPath"] == "/data"
            assert p["args2_6"]["systemLog"]["destination"] == "file"
            assert p["args2_6"]["systemLog"]["path"] == "/var/log/mongodb-mms-automation/mongodb.log"
            assert p["logRotate"]["sizeThresholdMB"] == 100
            assert p["logRotate"]["timeThresholdHrs"] == 1

    def test_om_replica_set(self):
        config = self.get_automation_config()
        rs = config["replicaSets"]
        assert rs[0]["_id"] == RESOURCE_NAME

        for idx in range(0, 4):
            m = rs[0]["members"][idx]
            assert m["_id"] == idx
            assert m["arbiterOnly"] is False
            assert m["hidden"] is False
            assert m["priority"] == 1.0
            assert m["votes"] == 1
            assert m["buildIndexes"] is True
            assert m["host"] == f"my-replica-set-{idx}"

    def test_monitoring_versions(self, cluster_domain: str):
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 5

        # Monitoring agent is installed on all hosts
        for i in range(0, 5):
            if "baseUrl" in mv[i]:
                assert mv[i]["baseUrl"] is None
            hostname = "my-replica-set-{}.my-replica-set-svc.{}.svc.{}".format(i, self.namespace, cluster_domain)
            assert mv[i]["hostname"] == hostname
            assert mv[i]["name"] == DEFAULT_MONITORING_AGENT_VERSION

    def test_backup(self, cluster_domain: str):
        config = self.get_automation_config()
        # 1 backup agent per host
        bkp = config["backupVersions"]
        assert len(bkp) == 5

        # Backup agent is installed on all hosts
        for i in range(0, 5):
            hostname = "{resource_name}-{idx}.{resource_name}-svc.{namespace}.svc.{cluster_domain}".format(
                resource_name=RESOURCE_NAME, idx=i, namespace=self.namespace, cluster_domain=cluster_domain
            )
            assert bkp[i]["hostname"] == hostname
            assert bkp[i]["name"] == DEFAULT_BACKUP_VERSION


@pytest.mark.e2e_replica_set
def test_replica_set_can_be_scaled_down_and_connectable(replica_set: MongoDB):
    """Makes sure that scaling down 5->3 members still reaches a Running & connectable state."""
    replica_set["spec"]["members"] = 3
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=1000)

    actester = AutomationConfigTester(KubernetesTester.get_automation_config())

    assert len(actester.get_replica_set_processes(RESOURCE_NAME)) == 3

    assert replica_set["status"]["members"] == 3

    replica_set.assert_connectivity()


@pytest.mark.e2e_replica_set
class TestReplicaSetDelete(KubernetesTester):
    """
    name: Replica Set Deletion
    tags: replica-set, removal
    description: |
      Deletes a Replica Set.
    delete:
      file: replica-set.yaml
      wait_until: mongo_resource_deleted
    """

    def test_replica_set_sts_doesnt_exist(self):
        """The StatefulSet must be removed by Kubernetes as soon as the MongoDB resource is removed.
        Note, that this may lag sometimes (caching or whatever?) and it's more safe to wait a bit"""
        time.sleep(15)
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)

    def test_service_does_not_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service(RESOURCE_NAME + "-svc", self.namespace)


def assert_container_env_vars(namespace: str, pod_name: str):
    pod = client.CoreV1Api().read_namespaced_pod(pod_name, namespace)
    c0 = pod.spec.containers[0]
    for envvar in c0.env:
        if envvar.name == "AGENT_API_KEY":
            assert envvar.value is None, "cannot configure value and value_from"
            assert envvar.value_from.secret_key_ref.name == f"{_get_group_id(c0.env)}-group-secret"
            assert envvar.value_from.secret_key_ref.key == "agentApiKey"
            continue

        assert envvar.name in [
            "AGENT_FLAGS",
            "BASE_URL",
            "GROUP_ID",
            "USER_LOGIN",
            "LOG_LEVEL",
            "SSL_TRUSTED_MMS_SERVER_CERTIFICATE",
            "SSL_REQUIRE_VALID_MMS_CERTIFICATES",
            "MULTI_CLUSTER_MODE",
            "MDB_LOG_FILE_AUTOMATION_AGENT_VERBOSE",
            "MDB_LOG_FILE_AUTOMATION_AGENT_STDERR",
            "MDB_LOG_FILE_AUTOMATION_AGENT",
            "MDB_LOG_FILE_MONITORING_AGENT",
            "MDB_LOG_FILE_BACKUP_AGENT",
            "MDB_LOG_FILE_MONGODB",
            "MDB_LOG_FILE_MONGODB_AUDIT",
            "MDB_STATIC_CONTAINERS_ARCHITECTURE",
        ]
        assert envvar.value is not None or envvar.name == "AGENT_FLAGS"
