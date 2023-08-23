import kubernetes
import kubernetes.client
import datetime
from pytest import mark, fixture
from kubetester.awss3client import AwsS3Client, s3_endpoint
import time
from kubetester import (
    create_or_update,
    create_or_update_configmap,
)
from kubetester import try_load

from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
)
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.omtester import OMTester

from tests.multicluster.conftest import cluster_spec_list

from tests.opsmanager.om_ops_manager_backup import (
    AWS_REGION,
    create_aws_secret,
    create_s3_bucket,
)
from pymongo.errors import ServerSelectionTimeoutError


TEST_DATA = {"name": "John", "address": "Highway 37", "age": 30}
MONGODB_PORT = 30000

S3_OPLOG_NAME = "s3-oplog"
S3_BLOCKSTORE_NAME = "s3-blockstore"
USER_PASSWORD = "/qwerty@!#:"


@fixture(scope="module")
def s3_bucket_oplog(
    aws_s3_client: AwsS3Client,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    create_aws_secret(aws_s3_client, S3_OPLOG_NAME + "-secret", namespace, central_cluster_client)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def s3_bucket_blockstore(
    aws_s3_client: AwsS3Client,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    create_aws_secret(aws_s3_client, S3_BLOCKSTORE_NAME + "-secret", namespace, central_cluster_client)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]


def create_project_config_map(om: MongoDBOpsManager, mdb_name, project_name, client, custom_ca):
    name = f"{mdb_name}-config"
    data = {
        "baseUrl": om.om_status().get_url(),
        "projectName": project_name,
        "sslMMSCAConfigMap": custom_ca,
        "orgId": "",
    }

    create_or_update_configmap(om.namespace, name, data, client)


@fixture(scope="module")
def multi_cluster_s3_replica_set(
    ops_manager,
    namespace,
    central_cluster_client: kubernetes.client.ApiClient,
    appdb_member_cluster_names: list[str],
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi-cluster.yaml"), "multi-replica-set", namespace
    ).configure(ops_manager, "s3metadata", api_client=central_cluster_client)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(appdb_member_cluster_names, [1, 2])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield create_or_update(resource)


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket_oplog: str,
    s3_bucket_blockstore: str,
    central_cluster_client: kubernetes.client.ApiClient,
    appdb_member_cluster_names: list[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:

    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_tls_s3.yaml"), namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    resource.allow_mdb_rc_versions()

    del resource["spec"]["security"]
    del resource["spec"]["applicationDatabase"]["security"]

    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "clusterSpecList": cluster_spec_list(appdb_member_cluster_names, [1, 2]),
        "agent": {"logLevel": "DEBUG"},
        "version": custom_appdb_version,
    }

    # configure S3 Blockstore
    resource["spec"]["backup"]["s3Stores"][0]["name"] = S3_BLOCKSTORE_NAME
    resource["spec"]["backup"]["s3Stores"][0]["s3SecretRef"]["name"] = S3_BLOCKSTORE_NAME + "-secret"
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket_blockstore
    resource["spec"]["backup"]["s3Stores"][0]["s3RegionOverride"] = AWS_REGION

    # configure S3 Oplog
    resource["spec"]["backup"]["s3OpLogStores"][0]["name"] = S3_OPLOG_NAME
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3SecretRef"]["name"] = S3_OPLOG_NAME + "-secret"
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketName"] = s3_bucket_oplog
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3RegionOverride"] = AWS_REGION

    resource.create_admin_secret(api_client=central_cluster_client)

    try_load(resource)
    return resource


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_s3_based_backup_restore
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled.
    """

    def test_create_om(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager["spec"]["backup"]["members"] = 1
        create_or_update(ops_manager)

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)

    def test_om_is_running(self, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient):
        # at this point AppDB is used as the "metadatastore"
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=1000, ignore_errors=True)
        om_tester = ops_manager.get_om_tester(api_client=central_cluster_client)
        om_tester.assert_healthiness()

    def test_add_metadatastore(
        self,
        multi_cluster_s3_replica_set: MongoDBMulti,
        ops_manager: MongoDBOpsManager,
    ):
        multi_cluster_s3_replica_set.assert_reaches_phase(Phase.Running, timeout=1000)

        # configure metadatastore in om, use dedicate MDB instead of AppDB
        ops_manager.load()
        ops_manager["spec"]["backup"]["s3Stores"][0]["mongodbResourceRef"] = {"name": multi_cluster_s3_replica_set.name}
        ops_manager["spec"]["backup"]["s3OpLogStores"][0]["mongodbResourceRef"] = {
            "name": multi_cluster_s3_replica_set.name
        }
        ops_manager.update()

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=10000)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=1000, ignore_errors=True)

    def test_om_s3_stores(self, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient):
        om_tester = ops_manager.get_om_tester(api_client=central_cluster_client)
        om_tester.assert_s3_stores([{"id": S3_BLOCKSTORE_NAME, "s3RegionOverride": AWS_REGION}])
        om_tester.assert_oplog_s3_stores([{"id": S3_OPLOG_NAME, "s3RegionOverride": AWS_REGION}])


@mark.e2e_multi_cluster_s3_based_backup_restore
class TestBackupForMongodb:
    @fixture(scope="module")
    def project_one(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ) -> OMTester:
        return ops_manager.get_om_tester(
            project_name=f"{namespace}-project-one",
            api_client=central_cluster_client,
        )

    @fixture(scope="module")
    def mongodb_multi_one_collection(self, mongodb_multi_one: MongoDBMulti):
        collection = mongodb_multi_one.tester(port=MONGODB_PORT).client["testdb"]
        return collection["testcollection"]

    @fixture(scope="module")
    def mongodb_multi_one(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
        namespace: str,
        appdb_member_cluster_names: list[str],
    ) -> MongoDBMulti:
        resource = MongoDBMulti.from_yaml(
            yaml_fixture("mongodb-multi.yaml"),
            "multi-replica-set-one",
            namespace
            # the project configmap should be created in the central cluster.
        ).configure(ops_manager, f"{namespace}-project-one", api_client=central_cluster_client)

        resource["spec"]["clusterSpecList"] = cluster_spec_list(appdb_member_cluster_names, [1, 2])

        # creating a cluster with backup should work with custom ports
        resource["spec"].update({"additionalMongodConfig": {"net": {"port": MONGODB_PORT}}})

        resource.configure_backup(mode="enabled")
        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

        return create_or_update(resource)

    def test_mongodb_multi_one_running_state(self, mongodb_multi_one: MongoDBMulti):
        # we might fail connection in the beginning since we set a custom dns in coredns
        mongodb_multi_one.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    @skip_if_local
    def test_add_test_data(self, mongodb_multi_one_collection):
        max_attempts = 100
        while max_attempts > 0:
            try:
                mongodb_multi_one_collection.insert_one(TEST_DATA)
                return
            except Exception as e:
                print(e)
                max_attempts -= 1
                time.sleep(6)

    def test_mdb_backed_up(self, project_one: OMTester):
        project_one.wait_until_backup_snapshots_are_ready(expected_count=1)

    def test_change_mdb_data(self, mongodb_multi_one_collection):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))
        time.sleep(30)
        mongodb_multi_one_collection.insert_one({"foo": "bar"})

    def test_pit_restore(self, project_one: OMTester):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))

        pit_datetme = datetime.datetime.now() - datetime.timedelta(seconds=15)
        pit_millis = time_to_millis(pit_datetme)
        print("Restoring back to the moment 15 seconds ago (millis): {}".format(pit_millis))

        project_one.create_restore_job_pit(pit_millis)

    def test_data_got_restored(self, mongodb_multi_one_collection):
        """The data in the db has been restored to the initial state. Note, that this happens eventually - so
        we need to loop for some time (usually takes 20 seconds max). This is different from restoring from a
        specific snapshot (see the previous class) where the FINISHED restore job means the data has been restored.
        For PIT restores FINISHED just means the job has been created and the agents will perform restore eventually
        """
        print("\nWaiting until the db data is restored")
        retries = 120
        while retries > 0:
            try:
                records = list(mongodb_multi_one_collection.find())
                assert records == [TEST_DATA]
                return
            except AssertionError:
                pass
            except ServerSelectionTimeoutError:
                # The mongodb driver complains with `No replica set members
                # match selector "Primary()",` This could be related with DNS
                # not being functional, or the database going through a
                # re-election process. Let's give it another chance to succeed.
                pass
            except Exception as e:
                # We ignore Exception as there is usually a blip in connection (backup restore
                # results in reelection or whatever)
                # "Connection reset by peer" or "not master and slaveOk=false"
                print("Exception happened while waiting for db data restore: ", e)
                # this is definitely the sign of a problem - no need continuing as each connection times out
                # after many minutes
                if "Connection refused" in str(e):
                    raise e
            retries -= 1
            time.sleep(1)

        print("\nExisting data in MDB: {}".format(list(mongodb_multi_one_collection.find())))

        raise AssertionError("The data hasn't been restored in 2 minutes!")


def time_to_millis(date_time) -> int:
    """https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis
