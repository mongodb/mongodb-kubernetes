from operator import attrgetter
from os import environ

from kubetester.kubetester import (
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    KubernetesTester.make_default_gp2_storage_class()

    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_localmode-multiple-pv.yaml"), namespace=namespace
    )
    if "CUSTOM_OM_VERSION" in environ:
        resource["spec"]["version"] = environ.get("CUSTOM_OM_VERSION")
    return resource.create()


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"), namespace=namespace,
    ).configure(ops_manager, "my-replica-set")
    resource["spec"]["version"] = "4.2.0"
    resource["spec"]["members"] = 2
    yield resource.create()


@mark.e2e_om_localmode_multiple_pv
class TestOpsManagerCreation:
    def test_ops_manager_ready(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)

    def test_volume_mounts(self, ops_manager: MongoDBOpsManager):
        statefulset = ops_manager.read_statefulset()

        # pod template has volume mount request
        assert ("/mongodb-ops-manager/mongodb-releases", "mongodb-versions") in (
            (mount.mount_path, mount.name)
            for mount in statefulset.spec.template.spec.containers[0].volume_mounts
        )

    def test_pvcs(self, ops_manager: MongoDBOpsManager):
        for pod in ops_manager.read_om_pods():
            claims = [
                volume
                for volume in pod.spec.volumes
                if getattr(volume, "persistent_volume_claim")
            ]
            assert len(claims) == 1

            KubernetesTester.check_single_pvc(
                namespace=ops_manager.namespace,
                volume=claims[0],
                expected_name="mongodb-versions",
                expected_claim_name="mongodb-versions-{}".format(pod.metadata.name),
                expected_size="20G",
                storage_class="gp2",
            )

    def test_replica_set_reaches_failed_phase(self, replica_set: MongoDB):
        # CLOUDP-61573 - we don't get the validation error on automation config submission if the OM has no
        # distros for local mode - so just wait until the agents don't reach goal state
        replica_set.assert_reaches_phase(Phase.Failed, timeout=300)

    def test_add_mongodb_distros_and_tools(self, ops_manager: MongoDBOpsManager):
        ops_manager.download_mongodb_binaries_and_tools("4.2.0")

    def test_replica_set_reaches_running_phase(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=300)

    def test_client_can_connect_to_mongodb(self, replica_set: MongoDB):
        replica_set.assert_connectivity()
        replica_set.tester().assert_version("4.2.0")


@mark.e2e_om_localmode_multiple_pv
class TestOpsManagerRestarted:
    def test_restart_ops_manager_pod(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["configuration"]["mms.testUtil.enabled"] = "false"
        ops_manager.update()
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_can_scale_replica_set(self, replica_set: MongoDB):
        replica_set["spec"]["members"] = 3
        replica_set.update()
        replica_set.assert_abandons_phase(Phase.Running)
        replica_set.assert_reaches_phase(Phase.Running, timeout=200)

    def test_client_can_still_connect(self, replica_set: MongoDB):
        replica_set.assert_connectivity()
