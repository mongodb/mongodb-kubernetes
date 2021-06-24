from typing import Optional

import yaml
import time
from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
    KubernetesTester,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark


VERSION_NOT_IN_WEB_SERVER = "4.2.1"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str]) -> MongoDBOpsManager:
    with open(yaml_fixture("remote_fixtures/nginx-config.yaml"), "r") as f:
        config_body = yaml.safe_load(f.read())
    KubernetesTester.clients("corev1").create_namespaced_config_map(
        namespace, config_body
    )

    with open(yaml_fixture("remote_fixtures/nginx.yaml"), "r") as f:
        nginx_body = yaml.safe_load(f.read())
    KubernetesTester.create_deployment(namespace, body=nginx_body)

    with open(yaml_fixture("remote_fixtures/nginx-svc.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    KubernetesTester.create_service(namespace, body=service_body)

    """ The fixture for Ops Manager to be created."""
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("remote_fixtures/om_remotemode.yaml"),
        namespace=namespace,
    )
    om["spec"]["configuration"]["automation.versions.source"] = "remote"
    om["spec"]["configuration"][
        "automation.versions.download.baseUrl"
    ] = f"http://nginx-svc.{namespace}.svc.cluster.local:80"

    om.set_version(custom_version)
    yield om.create()

    KubernetesTester.delete_configmap(namespace, "nginx-conf")
    KubernetesTester.delete_service(namespace, "nginx-svc")
    KubernetesTester.delete_deployment(namespace, "nginx-deployment")


@fixture(scope="module")
def replica_set(
    ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
    ).configure(ops_manager, "my-replica-set")
    resource["spec"]["version"] = custom_mdb_version
    yield resource.create()


@fixture(scope="module")
def replica_set_ent(
    ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="the-replica-set-ent",
    ).configure(ops_manager, "my-other-replica-set")
    resource["spec"]["version"] = custom_mdb_version + "-ent"
    yield resource.create()


@mark.e2e_om_remotemode
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)
    assert ops_manager.appdb_status().get_members() == 3


@skip_if_local
@mark.e2e_om_remotemode
def test_appdb_mongod(ops_manager: MongoDBOpsManager):
    mdb_tester = ops_manager.get_appdb_tester()
    mdb_tester.assert_connectivity()


@mark.e2e_om_remotemode
def test_ops_manager_reaches_running_phase(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)

    # CLOUDP-83792: some insight: OM has a number of Cron jobs and one of them is responsible for filtering the builds
    # returned in the automation config to include only the available ones (in remote/local modes).
    # Somehow though as of OM 4.4.9 this filtering didn't work fine and some Enterprise builds were not returned so
    # the replica sets using enterprise versions didn't reach the goal.
    # We need to sleep for sometime to let the cron get into the game and this allowed to reproduce the issue
    # (got fixed by switching off the cron by 'automation.versions.download.baseUrl.allowOnlyAvailableBuilds: false')
    print("Sleeping for one minute to let Ops Manager Cron jobs kick in")
    time.sleep(60)


@mark.e2e_om_remotemode
def test_replica_sets_reaches_running_phase(
    replica_set: MongoDB, replica_set_ent: MongoDB
):
    """ Doing this in parallel for faster success """
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)
    replica_set_ent.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_om_remotemode
def test_replica_set_reaches_failed_phase(replica_set: MongoDB):
    replica_set["spec"]["version"] = VERSION_NOT_IN_WEB_SERVER
    replica_set.update()

    # ReplicaSet times out attempting to fetch version from web server
    replica_set.assert_reaches_phase(Phase.Failed, timeout=200)


@mark.e2e_om_remotemode
def test_replica_set_recovers(replica_set: MongoDB, custom_mdb_version: str):
    replica_set["spec"]["version"] = custom_mdb_version
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_local
@mark.e2e_om_remotemode
def test_client_can_connect_to_mongodb(replica_set: MongoDB):
    replica_set.assert_connectivity()


@skip_if_local
@mark.e2e_om_remotemode
def test_client_can_connect_to_mongodb_ent(replica_set_ent: MongoDB):
    replica_set_ent.assert_connectivity()


@skip_if_local
@mark.e2e_om_remotemode
def test_client_can_connect_to_mongodb_ent(replica_set_ent: MongoDB):
    replica_set_ent.assert_connectivity()


@mark.e2e_om_remotemode
def test_restart_ops_manager_pod(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["configuration"]["mms.testUtil.enabled"] = "false"
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_remotemode
def test_can_scale_replica_set(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["members"] = 5
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_local
@mark.e2e_om_remotemode
def test_client_can_still_connect(replica_set: MongoDB):
    replica_set.assert_connectivity()


@skip_if_local
@mark.e2e_om_remotemode
def test_client_can_still_connect_to_ent(replica_set_ent: MongoDB):
    replica_set_ent.assert_connectivity()
