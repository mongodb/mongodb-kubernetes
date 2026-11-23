from datetime import datetime, timezone
from typing import Iterator, Optional

import pymongo
from kubetester import create_or_update_secret, read_secret, run_periodically, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.certs import create_tls_certs
from kubetester.kmip import KMIPDeployment
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo import ReadPreference
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.om_ops_manager_backup import S3_SECRET_NAME, create_aws_secret, create_s3_bucket
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}
OPLOG_SECRET_NAME = S3_SECRET_NAME + "-oplog"

MONGODB_CR_NAME = "mdb-latest"
MONGODB_CR_KMIP_TEST_PREFIX = "test-prefix"


def _process_rotation_timestamp(process: dict) -> Optional[datetime]:
    """The lastKmipMasterKeyRotation of a process document, or None when unset."""
    value = process.get("lastKmipMasterKeyRotation")
    if not value:
        return None
    if isinstance(value, str):
        value = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    if value.year == 1:
        # The zero value the agent records before the first rotation.
        return None
    return value


def _member_process_note(host: str) -> dict:
    """The local.clustermanager process note of one replica set member, read directly from that member."""
    client = pymongo.MongoClient(f"mongodb://{host}/?directConnection=true", serverSelectionTimeoutMS=120000)
    return client["local"]["clustermanager"].find_one() or {}


@fixture(scope="module")
def kmip(issuer, issuer_ca_configmap, namespace: str) -> KMIPDeployment:
    return KMIPDeployment(namespace, issuer, "ca-key-pair", issuer_ca_configmap).deploy()


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket")


@fixture(scope="module")
def oplog_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, OPLOG_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket-oplog")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    oplog_s3_bucket: str,
    issuer_ca_configmap: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_kmip.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["backup"]["encryption"]["kmip"]["server"]["ca"] = issuer_ca_configmap
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket

    resource["spec"]["backup"]["s3OpLogStores"] = [
        {
            "name": "s3Store2",
            "s3SecretRef": {
                "name": OPLOG_SECRET_NAME,
            },
            "pathStyleAccessEnabled": True,
            "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
            "s3BucketName": oplog_s3_bucket,
        }
    ]

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@fixture(scope="module")
def mdb_latest(
    ops_manager: MongoDBOpsManager,
    mdb_latest_kmip_secrets,
    namespace,
    custom_mdb_version: str,
):
    fixture_file_name = "replica-set-kmip.yaml"
    if is_default_architecture_static():
        fixture_file_name = "replica-set-kmip-static.yaml"
    resource = MongoDB.from_yaml(
        yaml_fixture(fixture_file_name),
        namespace=namespace,
        name=MONGODB_CR_NAME,
    ).configure(ops_manager, "mdbLatestProject")

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.configure_backup(mode="enabled")

    try_load(resource)
    return resource


@fixture(scope="module")
def mdb_latest_kmip_secrets(aws_s3_client: AwsS3Client, namespace, issuer, issuer_ca_configmap: str) -> str:
    mdb_latest_generated_kmip_certs_secret_name = create_tls_certs(
        issuer,
        namespace,
        MONGODB_CR_NAME,
        replicas=3,
        common_name=MONGODB_CR_NAME,
    )
    mdb_latest_generated_kmip_certs_secret = read_secret(namespace, mdb_latest_generated_kmip_certs_secret_name)
    mdb_secret_name = MONGODB_CR_KMIP_TEST_PREFIX + "-" + MONGODB_CR_NAME + "-kmip-client"
    create_or_update_secret(
        namespace,
        mdb_secret_name,
        {
            "tls.crt": mdb_latest_generated_kmip_certs_secret["tls.key"]
            + mdb_latest_generated_kmip_certs_secret["tls.crt"],
        },
        "tls",
    )
    return mdb_secret_name


@fixture(scope="module")
def mdb_latest_test_collection(mdb_latest):
    # we instantiate the pymongo client per test to avoid flakiness as the primary and secondary might swap
    collection = pymongo.MongoClient(mdb_latest.tester().cnx_string, **mdb_latest.tester().default_opts)["testdb"]
    return collection["testcollection"].with_options(read_preference=ReadPreference.PRIMARY_PREFERRED)


@fixture(scope="module")
def mdb_latest_project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name="mdbLatestProject")


@mark.e2e_om_ops_manager_backup_kmip
class TestOpsManagerCreation:
    def test_create_kmip(self, kmip: KMIPDeployment):
        kmip.status().assert_is_running()

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.backup_status().assert_reaches_phase(Phase.Pending)

    def test_s3_oplog_created(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_mdbs_created(self, mdb_latest: MongoDB, ops_manager: MongoDBOpsManager):
        # Once MDB is created, the OpsManager will be redeployed which may cause HTTP errors.
        # This is required to mount new secrets for KMIP. Having said that, we also need longer timeout.
        mdb_latest.update()
        mdb_latest.assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)


@mark.e2e_om_ops_manager_backup_kmip
class TestBackupForMongodb:
    def test_mdbs_created(self, mdb_latest: MongoDB):
        mdb_latest.assert_reaches_phase(Phase.Running)

    def test_add_test_data(self, mdb_latest_test_collection):
        mdb_latest_test_collection.insert_one(TEST_DATA)

    def test_mdbs_backed_up(self, mdb_latest_project: OMTester):
        # If OM is misconfigured, this will never become ready
        mdb_latest_project.wait_until_backup_snapshots_are_ready(expected_count=1, timeout=3500)

    def test_mdbs_backup_encrypted(self, mdb_latest_project: OMTester):
        # This type of testing has been agreed with Ops Manager / Backup Team
        cluster_id = mdb_latest_project.get_backup_cluster_id(expected_config_count=1)
        snapshots = mdb_latest_project.api_get_snapshots(cluster_id)
        assert snapshots[0]["parts"][0]["encryptionEnabled"]


@mark.e2e_om_ops_manager_backup_kmip
class TestKmipMasterKeyRotation:
    def test_agents_reach_goal_state_before_rotation(self, mdb_latest_project: OMTester):
        # The rotation request advances the automation config version, so the
        # baseline is every agent having applied the current one.
        mdb_latest_project.wait_agents_ready()

    def test_rotate_kmip_master_key(self, mdb_latest: MongoDB, mdb_latest_project: OMTester):
        # Ops Manager requests a master key rotation by publishing a new
        # lastKmipMasterKeyRotation timestamp for every mongod in the automation
        # config. This is the change Ops Manager stages when an operator selects
        # "Rotate the KMIP Master Key". The timestamp is only the request: every
        # agent must stop its mongod, run the one-shot rotation, restart the
        # mongod, and record the timestamp in the local process note before the
        # deployment can reach the new goal version.
        ac_tester = mdb_latest_project.get_automation_config_tester()
        ac = ac_tester.automation_config
        version_before = ac["version"]
        mongods = [process for process in ac["processes"] if process["processType"] == "mongod"]
        assert len(mongods) == mdb_latest["spec"]["members"]
        for process in mongods:
            # The agent only plans a rotation for a process with encryption enabled.
            ac_tester.assert_mdb_option(process, True, "security", "enableEncryption")
            assert _process_rotation_timestamp(process) is None, f"{process['name']} already rotated its master key"

        rotation_request = datetime.now(timezone.utc).replace(microsecond=0)
        for process in mongods:
            process["lastKmipMasterKeyRotation"] = rotation_request.isoformat().replace("+00:00", "Z")
        mdb_latest_project.api_put_automation_config(ac)

        # Publishing the timestamp must produce a new automation config version
        # that carries the request as the desired state of every member.
        published = mdb_latest_project.api_get_automation_config()
        assert published["version"] > version_before
        published_mongods = [process for process in published["processes"] if process["processType"] == "mongod"]
        assert len(published_mongods) == len(mongods)
        for process in published_mongods:
            assert _process_rotation_timestamp(process) == rotation_request

        # A member reaches the new goal version only after its agent completed
        # the RotateKmipMasterKey move, which stops the mongod, rotates the key
        # and writes the local process note. lastGoalVersionAchieved is
        # therefore the per-member evidence that the rotation was recorded.
        def every_member_completed_the_rotation():
            status: dict = mdb_latest_project.api_get_automation_status()
            goal_version = status.get("goalVersion")
            processes = status.get("processes", [])
            if not processes:
                return False, "the automation status has no processes"
            if goal_version is None or goal_version < published["version"]:
                return False, f"goalVersion={goal_version}, expected at least {published['version']}"
            pending = {
                process["name"]: {"achieved": process.get("lastGoalVersionAchieved"), "plan": process.get("plan")}
                for process in processes
                if process.get("lastGoalVersionAchieved") != goal_version
            }
            if pending:
                return False, f"members still rotating: {pending}"
            return True

        run_periodically(
            every_member_completed_the_rotation,
            timeout=1800,
            sleep_time=10,
            msg="every member to complete the KMIP master key rotation",
        )

        # The rotation restarts every mongod, so each member's uptime must be
        # younger than the request, and the replica set must be healthy.
        mdb_latest.assert_reaches_phase(Phase.Running, timeout=900)
        elapsed_seconds = (datetime.now(timezone.utc) - rotation_request).total_seconds()
        status = mdb_latest.tester().client.admin.command("replSetGetStatus", read_preference=ReadPreference.PRIMARY)
        members = status["members"]
        assert len(members) == len(mongods)
        assert all(member["health"] == 1 for member in members)
        assert any(member["stateStr"] == "PRIMARY" for member in members)
        for member in members:
            assert member["uptime"] < elapsed_seconds + 60, f"{member['name']} did not restart for the rotation"

        # The recorded counterpart of the published request is the local process
        # note of every member, which is not replicated. Read each member
        # directly: the agent writes the timestamp there after the rotation.
        for member in members:
            recorded = _process_rotation_timestamp(_member_process_note(member["name"]))
            assert recorded == rotation_request, f"{member['name']} recorded rotation timestamp {recorded}"

    def test_members_reachable_after_rotation(self, mdb_latest_project: OMTester):
        # The hosts API reports NO_DATA for a member the monitoring agent cannot reach.
        def all_members_reachable():
            unreachable = [
                host["hostname"]
                for host in mdb_latest_project.api_get_hosts()["results"]
                if host.get("typeName") == "NO_DATA"
            ]
            if unreachable:
                return False, f"members without monitoring data: {unreachable}"
            return True

        run_periodically(all_members_reachable, timeout=600, sleep_time=10, msg="all members to be reachable")

    def test_read_data_after_rotation(self, mdb_latest_test_collection):
        assert mdb_latest_test_collection.find_one({"_id": TEST_DATA["_id"]}) == TEST_DATA
