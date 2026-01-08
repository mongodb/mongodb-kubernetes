from typing import Optional

from kubetester import get_default_storage_class
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_localmode-multiple-pv.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
    ).configure(ops_manager, "my-replica-set")
    resource.set_version(custom_mdb_version)
    resource["spec"]["members"] = 2

    resource.update()
    return resource


@mark.e2e_om_localmode_multiple_pv
class TestOpsManagerCreation:
    def test_ops_manager_ready(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1000)

    def test_volume_mounts(self, ops_manager: MongoDBOpsManager):
        statefulset = ops_manager.read_statefulset()

        volume_mounts = [
            (mount.mount_path, mount.name) for mount in statefulset.spec.template.spec.containers[0].volume_mounts
        ]

        # pod template has volume mount request for mongodb-releases
        assert ("/mongodb-ops-manager/mongodb-releases", "mongodb-versions") in volume_mounts

        # pod template has volume mount request for /tmp (CLOUDP-339918)
        assert ("/tmp", "om-tmp") in volume_mounts

    def test_pvcs(self, ops_manager: MongoDBOpsManager):
        for api_client, pod in ops_manager.read_om_pods():
            claims = [volume for volume in pod.spec.volumes if getattr(volume, "persistent_volume_claim")]
            assert len(claims) == 1

            KubernetesTester.check_single_pvc(
                namespace=ops_manager.namespace,
                volume=claims[0],
                expected_name="mongodb-versions",
                expected_claim_name="mongodb-versions-{}".format(pod.metadata.name),
                expected_size="20G",
                storage_class=get_default_storage_class(),
                api_client=api_client,
            )

    def test_replica_set_reaches_failed_phase(self, replica_set: MongoDB):
        # CLOUDP-61573 - we don't get the validation error on automation config submission if the OM has no
        # distros for local mode - so just wait until the agents don't reach goal state
        replica_set.assert_reaches_phase(Phase.Failed, timeout=300)

    def test_add_mongodb_distros(self, ops_manager: MongoDBOpsManager, custom_mdb_version: str):
        ops_manager.download_mongodb_binaries(custom_mdb_version)

    # Since this is running OM in local mode, and OM6 is EOL, the latest mongodb versions are not available, unless we manually update the version manifest
    def test_update_om_version_manifest(self, ops_manager: MongoDBOpsManager):
        ops_manager.update_version_manifest()

    def test_replica_set_reaches_running_phase(self, replica_set: MongoDB):
        # note that the Replica Set may sometimes still get to Failed error
        # ("Status: 400 (Bad Request), Detail: Invalid config: MongoDB version 4.2.0 is not available.")
        # so we are ignoring errors during this wait
        replica_set.assert_reaches_phase(Phase.Running, timeout=300, ignore_errors=True)

    def test_client_can_connect_to_mongodb(self, replica_set: MongoDB, custom_mdb_version: str):
        replica_set.assert_connectivity()
        replica_set.tester().assert_version(custom_mdb_version)


@mark.e2e_om_localmode_multiple_pv
class TestOpsManagerRestarted:
    def test_restart_ops_manager_pod(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["configuration"]["mms.testUtil.enabled"] = "false"
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_can_scale_replica_set(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["members"] = 4
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=200)

    def test_client_can_still_connect(self, replica_set: MongoDB):
        replica_set.assert_connectivity()
