import pytest
import time
from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local, get_env_var_or_fail
from kubetester.mongotester import ReplicaSetTester

DEFAULT_BACKUP_VERSION = "6.6.0.959-1"

DEFAULT_MONITORING_AGENT_VERSION = "6.4.0.433-1"


def _get_group_id(envs) -> str:
    for e in envs:
        if e.name == "GROUP_ID":
            return e.value
    return ""


@pytest.mark.e2e_replica_set
class TestReplicaSetCreation(KubernetesTester):
    """
    name: Replica Set Creation
    tags: replica-set, creation
    description: |
      Creates a Replica set and checks everything is created as expected.
    create:
      file: replica-set.yaml
      wait_until: in_running_state
      timeout: 150
    """

    def test_replica_set_sts_exists(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)
        assert sts

    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 3
        assert sts.status.ready_replicas == 3

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)

        assert sts.metadata.name == "my-replica-set"
        assert sts.metadata.labels["app"] == "my-replica-set-svc"
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == "mongodb.com/v1"
        assert owner_ref0.kind == "MongoDB"
        assert owner_ref0.name == "my-replica-set"

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)
        assert sts.spec.replicas == 3

    def test_sts_template(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)

        tmpl = sts.spec.template
        assert tmpl.metadata.labels["app"] == "my-replica-set-svc"
        assert tmpl.metadata.labels["controller"] == "mongodb-enterprise-operator"
        assert tmpl.spec.service_account_name == "mongodb-enterprise-database-pods"
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
            assert c0.resources.limits["cpu"] == "500m"
            assert c0.resources.limits["memory"] == "700M"
            assert c0.resources.requests["cpu"] == "200m"
            assert c0.resources.requests["memory"] == "300M"

    def test_pods_container_envvars(self):
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            for envvar in c0.env:
                if envvar.name == "AGENT_API_KEY":
                    assert envvar.value is None, "cannot configure value and value_from"
                    assert (
                        envvar.value_from.secret_key_ref.name
                        == f"{_get_group_id(c0.env)}-group-secret"
                    )
                    assert envvar.value_from.secret_key_ref.key == "agentApiKey"
                    continue

                assert envvar.name in [
                    "BASE_URL",
                    "GROUP_ID",
                    "USER_LOGIN",
                    "LOG_LEVEL",
                    "SSL_TRUSTED_MMS_SERVER_CERTIFICATE",
                    "SSL_REQUIRE_VALID_MMS_CERTIFICATES",
                ]
                assert envvar.value is not None

    def test_service_is_created(self):
        svc = self.corev1.read_namespaced_service("my-replica-set-svc", self.namespace)
        assert svc

    def test_nodeport_service_not_exists(self):
        """Test that replica set is not exposed externally."""
        services = self.clients("corev1").list_namespaced_service(self.get_namespace())

        # 1 for replica set and 1 for validation webhook
        assert len(services.items) == 2
        assert len([s for s in services.items if s.spec.type == "NodePort"]) == 0

    def test_security_context_pods(self):
        for podname in self._get_pods("my-replica-set-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            pod_security_context = pod.spec.security_context
            security_context = pod.spec.containers[0].security_context
            if get_env_var_or_fail("MANAGED_SECURITY_CONTEXT") == "false":
                assert security_context.run_as_user == 2000
                assert security_context.run_as_non_root
                assert pod_security_context.fs_group == 2000
            else:
                # Note, that this code is a bit fragile as may depend on the version of Openshift, but we need to verify
                # that this is not "our" security context but the one generated by Openshift
                assert security_context.run_as_user is None
                assert security_context.run_as_non_root is None
                assert security_context.se_linux_options is not None
                assert pod_security_context.fs_group is not None
                assert pod_security_context.fs_group != 2000

    def test_security_context_operator(self):
        # todo there should be a better way to find the pods for deployment
        response = self.corev1.list_namespaced_pod(self.namespace)
        operator_pod = [
            pod
            for pod in response.items
            if pod.metadata.name.startswith("mongodb-enterprise-operator-")
        ][0]
        security_context = operator_pod.spec.security_context
        if get_env_var_or_fail("MANAGED_SECURITY_CONTEXT") == "false":
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

    def test_om_processes(self):
        config = self.get_automation_config()
        processes = config["processes"]
        p0 = processes[0]
        p1 = processes[1]
        p2 = processes[2]

        # First Process
        assert p0["name"] == "my-replica-set-0"
        assert p0["processType"] == "mongod"
        assert p0["version"] == "3.6.9"
        assert p0["authSchemaVersion"] == 5
        assert p0["featureCompatibilityVersion"] == "3.6"
        assert p0[
            "hostname"
        ] == "my-replica-set-0.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p0["args2_6"]["net"]["port"] == 27017
        assert p0["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p0["args2_6"]["storage"]["dbPath"] == "/data"
        assert p0["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p0["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p0["logRotate"]["sizeThresholdMB"] == 1000
        assert p0["logRotate"]["timeThresholdHrs"] == 24

        # Second Process
        assert p1["name"] == "my-replica-set-1"
        assert p1["processType"] == "mongod"
        assert p1["version"] == "3.6.9"
        assert p1["authSchemaVersion"] == 5
        assert p1["featureCompatibilityVersion"] == "3.6"
        assert p1[
            "hostname"
        ] == "my-replica-set-1.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p1["args2_6"]["net"]["port"] == 27017
        assert p1["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p1["args2_6"]["storage"]["dbPath"] == "/data"
        assert p1["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p1["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p1["logRotate"]["sizeThresholdMB"] == 1000
        assert p1["logRotate"]["timeThresholdHrs"] == 24

        # Third Process
        assert p2["name"] == "my-replica-set-2"
        assert p2["processType"] == "mongod"
        assert p2["version"] == "3.6.9"
        assert p2["authSchemaVersion"] == 5
        assert p2["featureCompatibilityVersion"] == "3.6"
        assert p2[
            "hostname"
        ] == "my-replica-set-2.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p2["args2_6"]["net"]["port"] == 27017
        assert p2["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p2["args2_6"]["storage"]["dbPath"] == "/data"
        assert p2["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p2["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p2["logRotate"]["sizeThresholdMB"] == 1000
        assert p2["logRotate"]["timeThresholdHrs"] == 24

    def test_om_replica_set(self):
        config = self.get_automation_config()
        rs = config["replicaSets"]
        assert rs[0]["_id"] == "my-replica-set"
        m0 = rs[0]["members"][0]
        m1 = rs[0]["members"][1]
        m2 = rs[0]["members"][2]

        # First Member
        assert m0["_id"] == 0
        assert m0["arbiterOnly"] is False
        assert m0["hidden"] is False
        assert m0["priority"] == 1
        assert m0["slaveDelay"] == 0
        assert m0["votes"] == 1
        assert m0["buildIndexes"] is True
        assert m0["host"] == "my-replica-set-0"

        # Second Member
        assert m1["_id"] == 1
        assert m1["arbiterOnly"] is False
        assert m1["hidden"] is False
        assert m1["priority"] == 1
        assert m1["slaveDelay"] == 0
        assert m1["votes"] == 1
        assert m1["buildIndexes"] is True
        assert m1["host"] == "my-replica-set-1"

        # Third Member
        assert m2["_id"] == 2
        assert m2["arbiterOnly"] is False
        assert m2["hidden"] is False
        assert m2["priority"] == 1
        assert m2["slaveDelay"] == 0
        assert m2["votes"] == 1
        assert m2["buildIndexes"] is True
        assert m2["host"] == "my-replica-set-2"

    def test_monitoring_versions(self):
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 3

        # Monitoring agent is installed on all hosts
        for i in range(0, 3):
            # baseUrl is not present in Cloud Manager response
            if "baseUrl" in mv[i]:
                assert mv[i]["baseUrl"] is None
            hostname = "my-replica-set-{}.my-replica-set-svc.{}.svc.cluster.local".format(
                i, self.namespace
            )
            assert mv[i]["hostname"] == hostname
            assert mv[i]["name"] == DEFAULT_MONITORING_AGENT_VERSION

    def test_backup(self):
        config = self.get_automation_config()
        # 1 backup agent per host
        bkp = config["backupVersions"]
        assert len(bkp) == 3

        # Backup agent is installed on all hosts
        for i in range(0, 3):
            hostname = "my-replica-set-{}.my-replica-set-svc.{}.svc.cluster.local".format(
                i, self.namespace
            )
            assert bkp[i]["hostname"] == hostname
            assert bkp[i]["name"] == DEFAULT_BACKUP_VERSION

    @skip_if_local
    def test_replica_set_was_configured(self):
        ReplicaSetTester("my-replica-set", 3, ssl=False).assert_connectivity()

    @skip_if_local
    def test_replica_set_was_configured_with_srv(self):
        ReplicaSetTester("my-replica-set", 3, ssl=False, srv=True).assert_connectivity()


@pytest.mark.e2e_replica_set
class TestReplicaSetUpdate(KubernetesTester):
    """
    name: Replica Set Updates
    tags: replica-set, scale, update
    description: |
      Updates a Replica Set to 5 members.
    update:
      file: replica-set.yaml
      patch: '[{"op":"replace","path":"/spec/members","value":5}]'
      wait_until: in_running_state
      timeout: 150
    """

    def test_replica_set_sts_should_exist(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)
        assert sts

    def test_sts_update(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 5
        assert sts.status.ready_replicas == 5

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)

        assert sts.metadata.name == "my-replica-set"
        assert sts.metadata.labels["app"] == "my-replica-set-svc"
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == "mongodb.com/v1"
        assert owner_ref0.kind == "MongoDB"
        assert owner_ref0.name == "my-replica-set"

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)
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
        for podname in self._get_pods("my-replica-set-{}", 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            for envvar in c0.env:
                if envvar.name == "AGENT_API_KEY":
                    assert envvar.value is None, "cannot configure value and value_from"
                    assert (
                        envvar.value_from.secret_key_ref.name
                        == f"{_get_group_id(c0.env)}-group-secret"
                    )
                    assert envvar.value_from.secret_key_ref.key == "agentApiKey"
                    continue

                assert envvar.name in [
                    "BASE_URL",
                    "GROUP_ID",
                    "USER_LOGIN",
                    "LOG_LEVEL",
                    "SSL_TRUSTED_MMS_SERVER_CERTIFICATE",
                    "SSL_REQUIRE_VALID_MMS_CERTIFICATES",
                ]
                assert envvar.value is not None

    def test_service_is_created(self):
        svc = self.corev1.read_namespaced_service("my-replica-set-svc", self.namespace)
        assert svc

    def test_om_processes_are_created(self):
        config = self.get_automation_config()
        assert len(config["processes"]) == 5

    def test_om_replica_set_is_created(self):
        config = self.get_automation_config()
        assert len(config["replicaSets"]) == 1

    def test_om_processes(self):
        config = self.get_automation_config()
        processes = config["processes"]
        p0 = processes[0]
        p1 = processes[1]
        p2 = processes[2]
        p3 = processes[3]
        p4 = processes[4]

        # First Process
        assert p0["name"] == "my-replica-set-0"
        assert p0["processType"] == "mongod"
        assert p0["version"] == "3.6.9"
        assert p0["authSchemaVersion"] == 5
        assert p0["featureCompatibilityVersion"] == "3.6"
        assert p0[
            "hostname"
        ] == "my-replica-set-0.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p0["args2_6"]["net"]["port"] == 27017
        assert p0["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p0["args2_6"]["storage"]["dbPath"] == "/data"
        assert p0["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p0["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p0["logRotate"]["sizeThresholdMB"] == 1000
        assert p0["logRotate"]["timeThresholdHrs"] == 24

        # Second Process
        assert p1["name"] == "my-replica-set-1"
        assert p1["processType"] == "mongod"
        assert p1["version"] == "3.6.9"
        assert p1["authSchemaVersion"] == 5
        assert p1["featureCompatibilityVersion"] == "3.6"
        assert p1[
            "hostname"
        ] == "my-replica-set-1.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p1["args2_6"]["net"]["port"] == 27017
        assert p1["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p1["args2_6"]["storage"]["dbPath"] == "/data"
        assert p1["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p1["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p1["logRotate"]["sizeThresholdMB"] == 1000
        assert p1["logRotate"]["timeThresholdHrs"] == 24

        # Third Process
        assert p2["name"] == "my-replica-set-2"
        assert p2["processType"] == "mongod"
        assert p2["version"] == "3.6.9"
        assert p2["authSchemaVersion"] == 5
        assert p2["featureCompatibilityVersion"] == "3.6"
        assert p2[
            "hostname"
        ] == "my-replica-set-2.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p2["args2_6"]["net"]["port"] == 27017
        assert p2["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p2["args2_6"]["storage"]["dbPath"] == "/data"
        assert p2["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p2["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p2["logRotate"]["sizeThresholdMB"] == 1000
        assert p2["logRotate"]["timeThresholdHrs"] == 24

        # Fourth Process
        assert p3["name"] == "my-replica-set-3"
        assert p3["processType"] == "mongod"
        assert p3["version"] == "3.6.9"
        assert p3["authSchemaVersion"] == 5
        assert p3["featureCompatibilityVersion"] == "3.6"
        assert p3[
            "hostname"
        ] == "my-replica-set-3.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p3["args2_6"]["net"]["port"] == 27017
        assert p3["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p3["args2_6"]["storage"]["dbPath"] == "/data"
        assert p3["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p3["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p3["logRotate"]["sizeThresholdMB"] == 1000
        assert p3["logRotate"]["timeThresholdHrs"] == 24

        # Fifth Process
        assert p4["name"] == "my-replica-set-4"
        assert p4["processType"] == "mongod"
        assert p4["version"] == "3.6.9"
        assert p4["authSchemaVersion"] == 5
        assert p4["featureCompatibilityVersion"] == "3.6"
        assert p4[
            "hostname"
        ] == "my-replica-set-4.my-replica-set-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p4["args2_6"]["net"]["port"] == 27017
        assert p4["args2_6"]["replication"]["replSetName"] == "my-replica-set"
        assert p4["args2_6"]["storage"]["dbPath"] == "/data"
        assert p4["args2_6"]["systemLog"]["destination"] == "file"
        assert (
            p4["args2_6"]["systemLog"]["path"]
            == "/var/log/mongodb-mms-automation/mongodb.log"
        )
        assert p4["logRotate"]["sizeThresholdMB"] == 1000
        assert p4["logRotate"]["timeThresholdHrs"] == 24

    def test_om_replica_set(self):
        config = self.get_automation_config()
        rs = config["replicaSets"]
        assert rs[0]["_id"] == "my-replica-set"
        m0 = rs[0]["members"][0]
        m1 = rs[0]["members"][1]
        m2 = rs[0]["members"][2]
        m3 = rs[0]["members"][3]
        m4 = rs[0]["members"][4]

        # First Member
        assert m0["_id"] == 0
        assert m0["arbiterOnly"] is False
        assert m0["hidden"] is False
        assert m0["priority"] == 1
        assert m0["slaveDelay"] == 0
        assert m0["votes"] == 1
        assert m0["buildIndexes"] is True
        assert m0["host"] == "my-replica-set-0"

        # Second Member
        assert m1["_id"] == 1
        assert m1["arbiterOnly"] is False
        assert m1["hidden"] is False
        assert m1["priority"] == 1
        assert m1["slaveDelay"] == 0
        assert m1["votes"] == 1
        assert m1["buildIndexes"] is True
        assert m1["host"] == "my-replica-set-1"

        # Third Member
        assert m2["_id"] == 2
        assert m2["arbiterOnly"] is False
        assert m2["hidden"] is False
        assert m2["priority"] == 1
        assert m2["slaveDelay"] == 0
        assert m2["votes"] == 1
        assert m2["buildIndexes"] is True
        assert m2["host"] == "my-replica-set-2"

        # Fourth Member
        assert m3["_id"] == 3
        assert m3["arbiterOnly"] is False
        assert m3["hidden"] is False
        assert m3["priority"] == 1
        assert m3["slaveDelay"] == 0
        assert m3["votes"] == 1
        assert m3["buildIndexes"] is True
        assert m3["host"] == "my-replica-set-3"

        # Fifth Member
        assert m4["_id"] == 4
        assert m4["arbiterOnly"] is False
        assert m4["hidden"] is False
        assert m4["priority"] == 1
        assert m4["slaveDelay"] == 0
        assert m4["votes"] == 1
        assert m4["buildIndexes"] is True
        assert m4["host"] == "my-replica-set-4"

    def test_monitoring_versions(self):
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 5

        # Monitoring agent is installed on all hosts
        for i in range(0, 5):
            if "baseUrl" in mv[i]:
                assert mv[i]["baseUrl"] is None
            hostname = "my-replica-set-{}.my-replica-set-svc.{}.svc.cluster.local".format(
                i, self.namespace
            )
            assert mv[i]["hostname"] == hostname
            assert mv[i]["name"] == DEFAULT_MONITORING_AGENT_VERSION

    def test_backup(self):
        config = self.get_automation_config()
        # 1 backup agent per host
        bkp = config["backupVersions"]
        assert len(bkp) == 5

        # Backup agent is installed on all hosts
        for i in range(0, 5):
            hostname = "my-replica-set-{}.my-replica-set-svc.{}.svc.cluster.local".format(
                i, self.namespace
            )
            assert bkp[i]["hostname"] == hostname
            assert bkp[i]["name"] == DEFAULT_BACKUP_VERSION


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
        """ The StatefulSet must be removed by Kubernetes as soon as the MongoDB resource is removed.
        Note, that this may lag sometimes (caching or whatever?) and it's more safe to wait a bit """
        time.sleep(15)
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set("my-replica-set", self.namespace)

    def test_service_does_not_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service("my-replica-set-svc", self.namespace)
