"""
The fist stage of an Operator-upgrade test.
It creates an OM instance with maximum features (backup, scram etc).
Also it creates a MongoDB referencing the OM.
"""
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDBOpsManager, Phase
from pytest import fixture, mark


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    """ The fixture for Ops Manager to be created."""
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_pod_spec.yaml"), namespace=namespace
    )
    return om.create()


@mark.e2e_om_ops_manager_pod_spec
class TestOpsManagerCreation:
    def test_om_created(self, ops_manager: MongoDBOpsManager):
        ops_manager.assert_reaches_phase(Phase.Running, timeout=900)

    def test_appdb_pod_template_containers(self, ops_manager: MongoDBOpsManager):
        appdb_sts = ops_manager.get_appdb_statefulset()
        assert len(appdb_sts.spec.template.spec.containers) == 2

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
        appdb_sts = ops_manager.get_appdb_statefulset()
        assert len(appdb_sts.spec.volume_claim_templates) == 1
        assert appdb_sts.spec.volume_claim_templates[0].metadata.name == "data"
        assert (
            appdb_sts.spec.volume_claim_templates[0].spec.resources.requests["storage"]
            == "1G"
        )

        for pod in ops_manager.get_appdb_pods():
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
        sts = ops_manager.get_statefulset()
        assert len(sts.spec.template.spec.containers) == 1
        om_container = sts.spec.template.spec.containers[0]
        assert om_container.resources.limits["cpu"] == "700m"
        assert om_container.resources.limits["memory"] == "6G"

        assert sts.spec.template.metadata.annotations == {"key1": "value1"}
        assert len(sts.spec.template.spec.tolerations) == 1
        assert sts.spec.template.spec.tolerations[0].key == "key"
        assert sts.spec.template.spec.tolerations[0].operator == "Exists"
        assert sts.spec.template.spec.tolerations[0].effect == "NoSchedule"

    def test_backup_pod_spec(self, ops_manager: MongoDBOpsManager):
        backup_sts = ops_manager.get_backup_statefulset()
        assert len(backup_sts.spec.template.spec.containers) == 1
        om_container = backup_sts.spec.template.spec.containers[0]
        assert om_container.resources.requests["cpu"] == "500m"
        assert om_container.resources.requests["memory"] == "4500M"

        assert len(backup_sts.spec.template.spec.host_aliases) == 1
        assert backup_sts.spec.template.spec.host_aliases[0].ip == "1.2.3.4"


@mark.e2e_om_ops_manager_pod_spec
class TestOpsManagerUpdate:
    def test_om_updated(self, ops_manager: MongoDBOpsManager):
        # adding annotations
        ops_manager["spec"]["applicationDatabase"]["podSpec"]["podTemplate"][
            "metadata"
        ] = {"annotations": {"annotation1": "val"}}

        # changing memory and adding labels for OM
        ops_manager["spec"]["podSpec"]["memory"] = "5G"
        ops_manager["spec"]["podSpec"]["podTemplate"]["metadata"]["labels"] = {
            "additional": "foo"
        }

        # termination_grace_period_seconds for Backup
        ops_manager["spec"]["backup"]["podSpec"]["podTemplate"]["spec"][
            "terminationGracePeriodSeconds"
        ] = 10

        print(ops_manager["spec"])

        ops_manager.update()
        ops_manager.assert_abandons_phase(Phase.Running)
        ops_manager.assert_reaches_phase(Phase.Running, timeout=900)

    def test_appdb_pod_template(self, ops_manager: MongoDBOpsManager):
        appdb_sts = ops_manager.get_appdb_statefulset()
        assert len(appdb_sts.spec.template.spec.containers) == 2

        appdb_container = appdb_sts.spec.template.spec.containers[0]
        assert appdb_container.name == "mongodb-enterprise-appdb"

        assert appdb_sts.spec.template.metadata.annotations == {"annotation1": "val"}

    def test_om_pod_spec(self, ops_manager: MongoDBOpsManager):
        sts = ops_manager.get_statefulset()
        assert len(sts.spec.template.spec.containers) == 1
        om_container = sts.spec.template.spec.containers[0]
        assert om_container.resources.limits["cpu"] == "700m"
        assert om_container.resources.limits["memory"] == "5G"

        assert sts.spec.template.metadata.annotations == {"key1": "value1"}
        assert len(sts.spec.template.metadata.labels) == 4
        assert sts.spec.template.metadata.labels["additional"] == "foo"
        assert len(sts.spec.template.spec.tolerations) == 1

    def test_backup_pod_spec(self, ops_manager: MongoDBOpsManager):
        backup_sts = ops_manager.get_backup_statefulset()

        assert len(backup_sts.spec.template.spec.host_aliases) == 1
        assert backup_sts.spec.template.spec.termination_grace_period_seconds == 10
