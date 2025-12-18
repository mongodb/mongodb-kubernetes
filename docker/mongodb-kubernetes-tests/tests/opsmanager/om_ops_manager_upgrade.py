from time import sleep
from typing import Optional

import pytest
import semver
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_default_architecture_static, run_periodically, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.conftest import get_member_cluster_clients, is_multi_cluster
from tests.opsmanager.om_appdb_scram import OM_USER_NAME
from tests.opsmanager.om_ops_manager_backup import OPLOG_RS_NAME, S3_SECRET_NAME, create_aws_secret, create_s3_bucket
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

# Current test focuses on Ops Manager upgrade which involves upgrade for both OpsManager and AppDB.
# MongoDBs are also upgraded. In case of major OM version upgrade (5.x -> 6.x) agents are expected to be upgraded
# for the existing MongoDBs.

logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket-")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket: str,
    custom_om_prev_version: str,
    custom_mdb_prev_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_upgrade.yaml"), namespace=namespace
    )

    if try_load(resource):
        return resource

    resource.allow_mdb_rc_versions()
    resource.set_version(custom_om_prev_version)
    resource.set_appdb_version(ensure_ent_version(custom_mdb_prev_version))
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_prev_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development-oplog")
    resource.set_version(custom_mdb_prev_version)

    try_load(resource)
    return resource


@fixture(scope="module")
def mdb(ops_manager: MongoDBOpsManager, custom_mdb_prev_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name="my-replica-set",
    )
    resource.set_version(custom_mdb_prev_version)
    resource.configure(ops_manager, "development")
    try_load(resource)
    return resource


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerCreation:
    """
    Creates an Ops Manager instance with AppDB of size 3.
    """

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        logger.info(f"Creating OM with version {ops_manager.get_version()}")
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    def test_gen_key_secret(self, ops_manager: MongoDBOpsManager):
        for member_cluster_name in ops_manager.get_om_member_cluster_names():
            secret = ops_manager.read_gen_key_secret(member_cluster_name=member_cluster_name)
            data = secret.data
            assert "gen.key" in data

    def test_admin_key_secret(self, ops_manager: MongoDBOpsManager):
        secret = ops_manager.read_api_key_secret()
        data = secret.data
        assert "publicKey" in data
        assert "privateKey" in data

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager, custom_om_prev_version: str):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_version(custom_om_prev_version)

        om_tester.assert_test_service()
        try:
            om_tester.assert_support_page_enabled()
            pytest.xfail("mms.helpAndSupportPage.enabled is expected to be false")
        except AssertionError:
            pass

    def test_appdb_scram_sha(self, ops_manager: MongoDBOpsManager):
        auto_generated_password = ops_manager.read_appdb_generated_password()
        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled("MONGODB-CR", False)
        automation_config_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", False)
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-1"
        )
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-256"
        )

    def test_generations(self, ops_manager: MongoDBOpsManager):
        ops_manager.reload()
        # The below two should be a no-op but in order to make this test rock solid, we need to ensure that everything
        # is running without any interruptions.
        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

        assert ops_manager.appdb_status().get_observed_generation() == 1
        assert ops_manager.om_status().get_observed_generation() == 1
        assert ops_manager.backup_status().get_observed_generation() == 1


@pytest.mark.e2e_om_ops_manager_upgrade
class TestBackupCreation:
    def test_oplog_mdb_created(
        self,
        oplog_replica_set: MongoDB,
    ):
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_add_oplog_config(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"] = [
            {"name": "oplog1", "mongodbResourceRef": {"name": "my-mongodb-oplog"}}
        ]
        ops_manager.update()

    def test_backup_is_enabled(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            ignore_errors=True,
        )

    def test_generations(self, ops_manager: MongoDBOpsManager):
        ops_manager.reload()

        # The below two should be a no-op but in order to make this test rock solid, we need to ensure that everything
        # is running without any interruptions.
        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running)

        assert ops_manager.appdb_status().get_observed_generation() == 2
        assert ops_manager.om_status().get_observed_generation() == 2
        assert ops_manager.backup_status().get_observed_generation() == 2


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerWithMongoDB:
    def test_mongodb_create(self, mdb: MongoDB, custom_mdb_prev_version: str):
        mdb.update()

        mdb.assert_reaches_phase(Phase.Running, timeout=600)
        mdb.assert_connectivity()
        mdb.tester().assert_version(custom_mdb_prev_version)

    def test_mongodb_upgrade(self, mdb: MongoDB):
        """Scales up the mongodb. Note, that we are not upgrading the Mongodb version at this stage as it can be
        the major update (e.g. 4.2 -> 4.4) and this requires OM upgrade as well - this happens later."""
        mdb.reload()
        mdb["spec"]["members"] = 4

        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=900)
        mdb.assert_connectivity()
        assert mdb.get_status_members() == 4


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerConfigurationChange:
    """
    The OM configuration changes: one property is removed, another is added.
    Note, that this is quite an artificial change to make it testable, these properties affect the behaviour of different
    endpoints in Ops Manager, so we can then check if the changes were propagated to OM
    """

    def test_restart_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["configuration"]["mms.testUtil.enabled"] = ""
        ops_manager["spec"]["configuration"]["mms.helpAndSupportPage.enabled"] = "true"
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    def test_keys_not_modified(
        self,
        ops_manager: MongoDBOpsManager,
        gen_key_resource_version: str,
        admin_key_resource_version: str,
    ):
        """Making sure that the new reconciliation hasn't tried to generate new gen and api keys"""
        gen_key_secret = ops_manager.read_gen_key_secret()
        api_key_secret = ops_manager.read_api_key_secret()

        assert gen_key_secret.metadata.resource_version == gen_key_resource_version
        assert api_key_secret.metadata.resource_version == admin_key_resource_version

    def test_om(self, ops_manager: MongoDBOpsManager):
        """Checks that the OM is responsive and test service is not available"""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_support_page_enabled()
        try:
            om_tester.assert_test_service()
            pytest.xfail("mms.testUtil.enabled is expected to be false")
        except AssertionError:
            pass

    def test_generations(self, ops_manager: MongoDBOpsManager):
        ops_manager.reload()

        # The below two should be a no-op but in order to make this test rock solid, we need to ensure that everything
        # is running without any interruptions.
        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running)

        assert ops_manager.appdb_status().get_observed_generation() == 3
        assert ops_manager.om_status().get_observed_generation() == 3
        assert ops_manager.backup_status().get_observed_generation() == 3


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerVersionUpgrade:
    """
    The OM version is upgraded - this means the new image is deployed for both OM and appdb.
    """

    agent_version = None

    def test_agent_version(self, mdb: MongoDB):
        if is_default_architecture_static():
            # Containers will not call the upgrade endpoint. Therefore, agent_version is not part of AC
            pod = client.CoreV1Api().read_namespaced_pod(mdb.name + "-0", mdb.namespace)
            image_tag = pod.spec.containers[0].image.split(":")[-1]
            TestOpsManagerVersionUpgrade.agent_version = image_tag

        else:
            TestOpsManagerVersionUpgrade.agent_version = mdb.get_automation_config_tester().get_agent_version()

    def test_upgrade_om_version(
        self,
        ops_manager: MongoDBOpsManager,
        custom_version: Optional[str],
        custom_appdb_version: str,
    ):
        logger.info(f"Upgrading OM from {ops_manager.get_version()} to {custom_version}")
        ops_manager.load()
        # custom_version fixture loads CUSTOM_OM_VERSION env variable, which is set in context files with one of the
        # values ops_manager_60_latest or ops_manager_70_latest in .evergreen.yml
        ops_manager.set_version(custom_version)
        ops_manager.set_appdb_version(custom_appdb_version)

        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

    def test_image_url(self, ops_manager: MongoDBOpsManager):
        pods = ops_manager.read_om_pods()
        assert len(pods) == ops_manager.get_total_number_of_om_replicas()
        for _, pod in pods:
            assert ops_manager.get_version() in pod.spec.containers[0].image

    def test_om(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_version(ops_manager.get_version())

    def test_appdb(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        mdb_tester = ops_manager.get_appdb_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version(custom_appdb_version)

    def test_appdb_scram_sha(self, ops_manager: MongoDBOpsManager):
        auto_generated_password = ops_manager.read_appdb_generated_password()
        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled("MONGODB-CR", False)
        automation_config_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", False)
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-1"
        )
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-256"
        )


@pytest.mark.e2e_om_ops_manager_upgrade
class TestMongoDbsVersionUpgrade:
    def test_mongodb_upgrade(self, mdb: MongoDB, custom_mdb_version: str):
        """Ensures that the existing MongoDB works fine with the new Ops Manager (scales up one member)
        Some details:
        - in case of patch upgrade of OM the existing agent is guaranteed to work with the new OM - we don't require
        the upgrade of all the agents
        - in case of major/minor OM upgrade the agents MUST be upgraded before reconciling - so that's why the agents upgrade
        is enforced before MongoDB reconciliation (the OM reconciliation happened above will drop the 'agents.nextScheduledTime'
        counter and schedule an upgrade in the operator opsmanager reconcile)
        """

        mdb.assert_reaches_phase(Phase.Running, timeout=1200)
        # Because OM was not in running phase, this resource, mdb, was also not in
        # running phase. We will wait for it to come back before applying any changes.
        mdb.reload()

        # At this point all the agent versions should be up to date and we can perform the database version upgrade.
        mdb["spec"]["version"] = custom_mdb_version
        mdb.update()

        # After the Ops Manager Upgrade, there's no time guarantees when a new manifest will be downloaded.
        # Therefore, we may occasionally get "Invalid config: MongoDB version 8.0.0 is not available."
        # This shouldn't happen very often at our customers as upgrading OM and MDB is usually separate processes.
        mdb.assert_reaches_phase(Phase.Running, timeout=1200, ignore_errors=True)
        mdb.assert_connectivity()
        mdb.tester().assert_version(custom_mdb_version)

    def test_agents_upgraded(self, mdb: MongoDB, ops_manager: MongoDBOpsManager, custom_om_prev_version: str):
        # The agents were requested to get upgraded immediately after Ops Manager upgrade.
        # Note, that this happens only for OM major/minor upgrade, so we need to check only this case
        prev_version = semver.VersionInfo.parse(custom_om_prev_version)
        new_version = semver.VersionInfo.parse(ops_manager.get_version())
        if is_default_architecture_static():
            pod = client.CoreV1Api().read_namespaced_pod(mdb.name + "-0", mdb.namespace)
            image_tag = pod.spec.containers[0].image.split(":")[-1]
            if prev_version.major != new_version.major:
                assert TestOpsManagerVersionUpgrade.agent_version != image_tag
        else:
            if prev_version.major != new_version.major or prev_version.minor != new_version.minor:
                assert (
                    TestOpsManagerVersionUpgrade.agent_version != mdb.get_automation_config_tester().get_agent_version()
                )


@pytest.mark.e2e_om_ops_manager_upgrade
class TestAppDBScramShaUpdated:
    def test_appdb_reconcile(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["logLevel"] = "DEBUG"
        ops_manager.update()

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    @pytest.mark.skip(reason="re-enable when only SCRAM-SHA-256 is supported for the AppDB")
    def test_appdb_scram_sha_(self, ops_manager: MongoDBOpsManager):
        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", False)
        automation_config_tester.assert_authentication_mechanism_disabled("MONGODB-CR", False)


@pytest.mark.e2e_om_ops_manager_upgrade
class TestBackupDaemonVersionUpgrade:
    def test_upgrade_backup_daemon(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            ignore_errors=True,
        )

    def test_backup_daemon_image_url(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        for _, pod in ops_manager.read_backup_pods():
            assert ops_manager.get_version() in pod.spec.containers[0].image


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerRemoved:
    """
    Deletes an Ops Manager Custom resource and verifies that some of the dependant objects are removed
    """

    def test_opsmanager_deleted(self, ops_manager: MongoDBOpsManager):
        ops_manager.delete()

        def om_is_clean():
            try:
                ops_manager.load()
                return False
            except ApiException:
                return True

        run_periodically(om_is_clean, timeout=180)
        # Some strange race conditions/caching - the api key secret is still queryable right after OM removal
        # (in openshift mainly)
        sleep(20)

    def test_api_key_not_removed(self, ops_manager: MongoDBOpsManager):
        """The API key must not be removed - this is for situations when the appdb is persistent -
        so PVs may survive removal"""
        ops_manager.read_api_key_secret()

    def test_gen_key_not_removed(self, ops_manager: MongoDBOpsManager):
        """The gen key must not be removed - this is for situations when the appdb is persistent -
        so PVs may survive removal"""
        ops_manager.read_gen_key_secret()

    def test_om_sts_removed(self, ops_manager: MongoDBOpsManager):
        for member_cluster in get_member_cluster_clients():
            with pytest.raises(ApiException):
                ops_manager.read_statefulset(member_cluster_name=member_cluster.cluster_name)

    def test_om_appdb_removed(self, ops_manager: MongoDBOpsManager):
        for member_cluster in get_member_cluster_clients():
            with pytest.raises(ApiException):
                ops_manager.read_appdb_statefulset(member_cluster_name=member_cluster.cluster_name)
