"""
The fist stage of an Operator-upgrade test.
It creates an OM instance with maximum features (backup, scram etc).
Also it creates a MongoDB referencing the OM.
"""
from os import environ

from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.custom_podspec import assert_volume_mounts_are_equal
from pytest import fixture, mark


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    """ The fixture for Ops Manager to be created."""
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_pod_spec.yaml"), namespace=namespace
    )
    if "CUSTOM_OM_VERSION" in environ:
        om["spec"]["version"] = environ.get("CUSTOM_OM_VERSION")
    return om.create()


@mark.e2e_om_ops_manager_pod_spec
class TestOpsManagerCreation:
    def test_appdb_ready(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="StatefulSet not ready", timeout=100
        )
        ops_manager.appdb_status().assert_status_resource_not_ready(
            ops_manager.app_db_name(),
            msg_regexp="Not all the Pods are ready \(total: 3.*\)",
        )
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=500)
        ops_manager.appdb_status().assert_empty_status_resources_not_ready()

    def test_ops_manager_ready(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="StatefulSet not ready", timeout=100
        )
        ops_manager.om_status().assert_status_resource_not_ready(
            ops_manager.name, msg_regexp="Not all the Pods are ready \(total: 1.*\)"
        )
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)
        ops_manager.om_status().assert_empty_status_resources_not_ready()

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_backup_pending(self, ops_manager: MongoDBOpsManager):
        """ backup is not configured properly - but it's ok as we are testing Statefulset only"""
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="StatefulSet not ready", timeout=100
        )
        ops_manager.backup_status().assert_status_resource_not_ready(
            ops_manager.backup_daemon_name(),
            msg_regexp="Not all the Pods are ready \(total: 1.*\)",
        )
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending, msg_regexp=".*is required for backup.*", timeout=900
        )
        ops_manager.backup_status().assert_empty_status_resources_not_ready()

    def test_appdb_pod_template_containers(self, ops_manager: MongoDBOpsManager):
        appdb_sts = ops_manager.read_appdb_statefulset()
        assert len(appdb_sts.spec.template.spec.containers) == 2

        assert (
            appdb_sts.spec.template.spec.service_account_name
            == "mongodb-enterprise-appdb"
        )

        appdb_container = appdb_sts.spec.template.spec.containers[0]
        assert appdb_container.name == "mongodb-enterprise-appdb"
        assert appdb_container.resources.limits["cpu"] == "250m"
        assert appdb_container.resources.limits["memory"] == "350M"

        assert appdb_sts.spec.template.spec.containers[1].name == "appdb-sidecar"
        assert appdb_sts.spec.template.spec.containers[1].image == "busybox"
        assert appdb_sts.spec.template.spec.containers[1].command == ["sleep"]
        assert appdb_sts.spec.template.spec.containers[1].args == ["infinity"]

    def test_appdb_persistence(self, ops_manager: MongoDBOpsManager, namespace: str):
        # appdb pod volume claim template
        appdb_sts = ops_manager.read_appdb_statefulset()
        assert len(appdb_sts.spec.volume_claim_templates) == 1
        assert appdb_sts.spec.volume_claim_templates[0].metadata.name == "data"
        assert (
            appdb_sts.spec.volume_claim_templates[0].spec.resources.requests["storage"]
            == "1G"
        )

        for pod in ops_manager.read_appdb_pods():
            # pod volume claim
            expected_claim_name = f"data-{pod.metadata.name}"
            claims = [
                volume
                for volume in pod.spec.volumes
                if getattr(volume, "persistent_volume_claim")
            ]
            assert len(claims) == 1
            assert claims[0].name == "data"
            assert claims[0].persistent_volume_claim.claim_name == expected_claim_name

            # volume claim created
            pvc = client.CoreV1Api().read_namespaced_persistent_volume_claim(
                expected_claim_name, namespace
            )
            assert pvc.status.phase == "Bound"
            assert pvc.spec.resources.requests["storage"] == "1G"

    def test_om_pod_spec(self, ops_manager: MongoDBOpsManager):
        sts = ops_manager.read_statefulset()
        assert (
            sts.spec.template.spec.service_account_name
            == "mongodb-enterprise-ops-manager"
        )

        assert len(sts.spec.template.spec.containers) == 1
        om_container = sts.spec.template.spec.containers[0]
        assert om_container.resources.limits["cpu"] == "700m"
        assert om_container.resources.limits["memory"] == "6G"

        assert sts.spec.template.metadata.annotations["key1"] == "value1"
        assert len(sts.spec.template.spec.tolerations) == 1
        assert sts.spec.template.spec.tolerations[0].key == "key"
        assert sts.spec.template.spec.tolerations[0].operator == "Exists"
        assert sts.spec.template.spec.tolerations[0].effect == "NoSchedule"

    def test_om_container_override(self, ops_manager: MongoDBOpsManager):
        sts = ops_manager.read_statefulset()
        om_container = sts.spec.template.spec.containers[0].to_dict()
        # Readiness probe got 'failure_threshold' overridden, everything else is the same
        # New volume mount was added
        expected_spec = {
            "name": "mongodb-ops-manager",
            "readiness_probe": {
                "http_get": {
                    "host": None,
                    "http_headers": None,
                    "path": "/monitor/health",
                    "port": 8080,
                    "scheme": "HTTP",
                },
                "failure_threshold": 20,
                "timeout_seconds": 5,
                "period_seconds": 5,
                "success_threshold": 1,
                "initial_delay_seconds": 60,
                "_exec": None,
                "tcp_socket": None,
            },
            "volume_mounts": [
                {
                    "name": "gen-key",
                    "mount_path": "/mongodb-ops-manager/.mongodb-mms",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": True,
                },
                {
                    "name": "ops-manager-scripts",
                    "mount_path": "/opt/scripts",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": True,
                },
                {
                    "name": "test-volume",
                    "mount_path": "/somewhere",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                },
            ],
        }
        for k in expected_spec:
            if k == "volume_mounts":
                continue
            assert om_container[k] == expected_spec[k]

        assert_volume_mounts_are_equal(
            om_container["volume_mounts"], expected_spec["volume_mounts"]
        )

        # new volume was added and the old ones ('gen-key' and 'ops-manager-scripts') stayed there
        assert len(sts.spec.template.spec.volumes) == 3

    def test_backup_pod_spec(self, ops_manager: MongoDBOpsManager):
        backup_sts = ops_manager.read_backup_statefulset()
        assert (
            backup_sts.spec.template.spec.service_account_name
            == "mongodb-enterprise-ops-manager"
        )

        assert len(backup_sts.spec.template.spec.containers) == 1
        om_container = backup_sts.spec.template.spec.containers[0]
        assert om_container.resources.requests["cpu"] == "500m"
        assert om_container.resources.requests["memory"] == "4500M"

        assert len(backup_sts.spec.template.spec.host_aliases) == 1
        assert backup_sts.spec.template.spec.host_aliases[0].ip == "1.2.3.4"


@mark.e2e_om_ops_manager_pod_spec
class TestOpsManagerUpdate:
    def test_om_updated(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        # adding annotations
        ops_manager["spec"]["applicationDatabase"]["podSpec"]["podTemplate"][
            "metadata"
        ] = {"annotations": {"annotation1": "val"}}

        # changing memory and adding labels for OM
        ops_manager["spec"]["statefulSet"]["spec"]["template"]["spec"]["containers"][0][
            "resources"
        ]["limits"]["memory"] = "5G"
        ops_manager["spec"]["statefulSet"]["spec"]["template"]["metadata"]["labels"] = {
            "additional": "foo"
        }

        # termination_grace_period_seconds for Backup
        ops_manager["spec"]["backup"]["statefulSet"]["spec"]["template"]["spec"][
            "terminationGracePeriodSeconds"
        ] = 10

        ops_manager.update()

    def test_appdb_ready(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="StatefulSet not ready", timeout=100
        )
        ops_manager.appdb_status().assert_status_resource_not_ready(
            ops_manager.app_db_name(),
            msg_regexp="Not all the Pods are ready \(total: 3.*\)",
        )
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=500)
        ops_manager.appdb_status().assert_empty_status_resources_not_ready()

    def test_ops_manager_ready(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="StatefulSet not ready", timeout=100
        )
        ops_manager.om_status().assert_status_resource_not_ready(
            ops_manager.name, msg_regexp="Not all the Pods are ready \(total: 1.*\)"
        )
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)
        ops_manager.om_status().assert_empty_status_resources_not_ready()

    def test_backup_pending(self, ops_manager: MongoDBOpsManager):
        """ backup is not configured properly - but it's ok as we are testing Statefulset only"""
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="StatefulSet not ready", timeout=100
        )
        ops_manager.backup_status().assert_status_resource_not_ready(
            ops_manager.backup_daemon_name(),
            msg_regexp="Not all the Pods are ready \(total: 1.*\)",
        )
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending, msg_regexp=".*is required for backup.*", timeout=900
        )

    def test_appdb_pod_template(self, ops_manager: MongoDBOpsManager):
        appdb_sts = ops_manager.read_appdb_statefulset()
        assert len(appdb_sts.spec.template.spec.containers) == 2

        appdb_container = appdb_sts.spec.template.spec.containers[0]
        assert appdb_container.name == "mongodb-enterprise-appdb"

        assert appdb_sts.spec.template.metadata.annotations == {"annotation1": "val"}

    def test_om_pod_spec(self, ops_manager: MongoDBOpsManager):
        sts = ops_manager.read_statefulset()
        assert len(sts.spec.template.spec.containers) == 1
        om_container = sts.spec.template.spec.containers[0]
        assert om_container.resources.limits["cpu"] == "700m"
        assert om_container.resources.limits["memory"] == "5G"

        assert sts.spec.template.metadata.annotations["key1"] == "value1"
        assert len(sts.spec.template.metadata.labels) == 4
        assert sts.spec.template.metadata.labels["additional"] == "foo"
        assert len(sts.spec.template.spec.tolerations) == 1

    def test_backup_pod_spec(self, ops_manager: MongoDBOpsManager):
        backup_sts = ops_manager.read_backup_statefulset()

        assert len(backup_sts.spec.template.spec.host_aliases) == 1
        assert backup_sts.spec.template.spec.termination_grace_period_seconds == 10
