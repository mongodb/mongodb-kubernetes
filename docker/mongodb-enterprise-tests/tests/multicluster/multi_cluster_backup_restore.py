import kubernetes.client
from pymongo.errors import ServerSelectionTimeoutError

from kubetester import read_secret, create_secret, delete_secret, read_service
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.omtester import OMTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
import time
import datetime

TEST_DATA = {"name": "John", "address": "Highway 37", "age": 30}


@fixture(scope="module")
def base_url(
    ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
) -> str:
    ops_manager.load()
    external_svc_name = ops_manager.external_svc_name()
    svc = read_service(
        ops_manager.namespace, external_svc_name, api_client=central_cluster_client
    )
    hostname = svc.status.load_balancer.ingress[0].hostname
    return f"https://{hostname}:8443"


@fixture(scope="module")
def project_one(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    base_url: str,
) -> OMTester:
    return ops_manager.get_om_tester(
        project_name=f"{namespace}-project-one",
        api_client=central_cluster_client,
        base_url=base_url,
    )


@fixture(scope="module")
def mongodb_multi_one_collection(mongodb_multi_one: MongoDBMulti):
    collection = mongodb_multi_one.tester().client["testdb"]
    return collection["testcollection"]


@fixture(scope="module")
def ops_manager(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBOpsManager:
    """Return the static Ops Manager instance in the central cluster."""
    om = MongoDBOpsManager(name="om-backup", namespace="ops-manager")
    om.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    om.load()

    # copy the credentials secret used by the static OM instance.
    data = read_secret(
        "ops-manager", "my-credentials", api_client=central_cluster_client
    )
    delete_secret(namespace, "my-credentials", api_client=central_cluster_client)
    create_secret(namespace, "my-credentials", data, api_client=central_cluster_client)

    data = read_secret(
        "ops-manager",
        "ops-manager-om-backup-admin-key",
        api_client=central_cluster_client,
    )
    create_secret(
        namespace,
        "ops-manager-om-backup-admin-key",
        data,
        api_client=central_cluster_client,
    )

    return om


@fixture(scope="module")
def mongodb_multi_one(
    ops_manager: MongoDBOpsManager,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    base_url: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"),
        "multi-replica-set-one",
        namespace
        # the project configmap should be created in the central cluster.
    ).configure(
        ops_manager, f"{namespace}-project-one", api_client=central_cluster_client
    )

    # TODO: use a full 3 cluster RS with backup once the required agent changes have been made. Remove the below 3 lines
    spec_item_with_one_member = resource["spec"]["clusterSpecList"][0]
    spec_item_with_one_member["members"] = 1
    resource["spec"]["clusterSpecList"] = [spec_item_with_one_member]

    resource.configure_backup(mode="enabled")
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    data = KubernetesTester.read_configmap(
        namespace, "multi-replica-set-one-config", api_client=central_cluster_client
    )
    KubernetesTester.delete_configmap(
        namespace, "multi-replica-set-one-config", api_client=central_cluster_client
    )
    data["baseUrl"] = base_url
    data["sslMMSCAConfigMap"] = multi_cluster_issuer_ca_configmap
    KubernetesTester.create_configmap(
        namespace,
        "multi-replica-set-one-config",
        data,
        api_client=central_cluster_client,
    )

    return resource.create()


@mark.e2e_multi_cluster_backup_restore
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_backup_restore
def test_mongodb_multi_one_running_state(mongodb_multi_one: MongoDBMulti):
    mongodb_multi_one.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_multi_cluster_backup_restore
def test_add_test_data(mongodb_multi_one_collection):
    max_attempts = 100
    while max_attempts > 0:
        try:
            mongodb_multi_one_collection.insert_one(TEST_DATA)
            return
        except Exception as e:
            print(e)
            max_attempts -= 1
            time.sleep(6)


@mark.e2e_multi_cluster_backup_restore
def test_mdb_backed_up(project_one: OMTester):
    project_one.wait_until_backup_snapshots_are_ready(expected_count=1)


@mark.e2e_multi_cluster_backup_restore
def test_change_mdb_data(mongodb_multi_one_collection):
    now_millis = time_to_millis(datetime.datetime.now())
    print("\nCurrent time (millis): {}".format(now_millis))
    time.sleep(30)
    mongodb_multi_one_collection.insert_one({"foo": "bar"})


@mark.e2e_multi_cluster_backup_restore
def test_pit_restore(project_one: OMTester):
    now_millis = time_to_millis(datetime.datetime.now())
    print("\nCurrent time (millis): {}".format(now_millis))

    pit_datetme = datetime.datetime.now() - datetime.timedelta(seconds=15)
    pit_millis = time_to_millis(pit_datetme)
    print("Restoring back to the moment 15 seconds ago (millis): {}".format(pit_millis))

    project_one.create_restore_job_pit(pit_millis)


@mark.e2e_multi_cluster_backup_restore
def test_data_got_restored(mongodb_multi_one_collection):
    """The data in the db has been restored to the initial state. Note, that this happens eventually - so
    we need to loop for some time (usually takes 20 seconds max). This is different from restoring from a
    specific snapshot (see the previous class) where the FINISHED restore job means the data has been restored.
    For PIT restores FINISHED just means the job has been created and the agents will perform restore eventually"""
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

    print(
        "\nExisting data in MDB: {}".format(list(mongodb_multi_one_collection.find()))
    )

    raise AssertionError("The data hasn't been restored in 2 minutes!")


def time_to_millis(date_time) -> int:
    """https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis
