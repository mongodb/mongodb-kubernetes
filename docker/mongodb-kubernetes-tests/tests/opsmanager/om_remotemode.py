import time
from typing import Any, Dict, Optional

import yaml
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

VERSION_NOT_IN_WEB_SERVER = "4.2.1"


def add_mdb_version_to_deployment(deployment: Dict[str, Any], version: str):
    """
    Adds a new initContainer to `deployment` to download a particular MongoDB version.

    Please note that the initContainers will never fail, so it is fine to add version that don't
    exist for older distributions (like mdb5.0 in ubuntu1604).
    """
    mount_path = "/mongodb-ops-manager/mongodb-releases/linux"
    distros = ("rhel8", "rhel80", "ubuntu1604", "ubuntu1804")

    base_url_community = "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64"
    base_url_enterprise = "https://downloads.mongodb.com/linux/mongodb-linux-x86_64-enterprise"
    base_url = base_url_community
    if version.endswith("-ent"):
        # If version is enterprise, the base_url changes slightly
        base_url = base_url_enterprise
        version = version.replace("-ent", "")

    if "initContainers" not in deployment["spec"]["template"]["spec"]:
        deployment["spec"]["template"]["spec"]["initContainers"] = []

    for distro in distros:
        url = f"{base_url}-{distro}-{version}.tgz"
        curl_command = f"curl -LO {url} --output-dir {mount_path}"

        container = {
            "name": KubernetesTester.random_k8s_name(prefix="mdb-download"),
            "image": "curlimages/curl:latest",
            "command": ["sh", "-c", f"{curl_command} && true"],
            "volumeMounts": [
                {
                    "name": "mongodb-versions",
                    "mountPath": mount_path,
                }
            ],
        }
        deployment["spec"]["template"]["spec"]["initContainers"].append(container)


@fixture(scope="module")
def nginx(namespace: str, custom_mdb_version: str, custom_appdb_version: str):
    with open(yaml_fixture("remote_fixtures/nginx-config.yaml"), "r") as f:
        config_body = yaml.safe_load(f.read())
    KubernetesTester.clients("corev1").create_namespaced_config_map(namespace, config_body)

    with open(yaml_fixture("remote_fixtures/nginx.yaml"), "r") as f:
        nginx_body = yaml.safe_load(f.read())

        # Adds versions to Nginx deployment.
        new_versions = set()
        new_versions.add(custom_mdb_version)
        new_versions.add(custom_mdb_version + "-ent")
        new_versions.add(custom_appdb_version)

        for version in new_versions:
            add_mdb_version_to_deployment(nginx_body, version)

    KubernetesTester.create_deployment(namespace, body=nginx_body)

    with open(yaml_fixture("remote_fixtures/nginx-svc.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    KubernetesTester.create_service(namespace, body=service_body)


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str, nginx) -> MongoDBOpsManager:
    """The fixture for Ops Manager to be created."""
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("remote_fixtures/om_remotemode.yaml"),
        namespace=namespace,
    )
    om["spec"]["configuration"]["automation.versions.source"] = "remote"
    om["spec"]["configuration"][
        "automation.versions.download.baseUrl"
    ] = f"http://nginx-svc.{namespace}.svc.cluster.local:80"

    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om.allow_mdb_rc_versions()

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    om.update()
    return om


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
    ).configure(ops_manager, "my-replica-set")
    resource.set_version(custom_mdb_version)
    yield resource.create()


@fixture(scope="module")
def replica_set_ent(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="the-replica-set-ent",
    ).configure(ops_manager, "my-other-replica-set")
    resource.set_version(ensure_ent_version(custom_mdb_version))
    yield resource.create()


@mark.e2e_om_remotemode
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    assert ops_manager.appdb_status().get_members() == 3


@skip_if_local
@mark.e2e_om_remotemode
def test_appdb_mongod(ops_manager: MongoDBOpsManager):
    mdb_tester = ops_manager.get_appdb_tester()
    mdb_tester.assert_connectivity()


@mark.e2e_om_remotemode
def test_ops_manager_reaches_running_phase(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    # CLOUDP-83792: some insight: OM has a number of Cron jobs and one of them is responsible for filtering the builds
    # returned in the automation config to include only the available ones (in remote/local modes).
    # Somehow though as of OM 4.4.9 this filtering didn't work fine and some Enterprise builds were not returned so
    # the replica sets using enterprise versions didn't reach the goal.
    # We need to sleep for some time to let the cron get into the game and this allowed to reproduce the issue
    # (got fixed by switching off the cron by 'automation.versions.download.baseUrl.allowOnlyAvailableBuilds: false')
    print("Sleeping for one minute to let Ops Manager Cron jobs kick in")
    time.sleep(60)


# Since OM6 is EOL, the latest mongodb versions are not available, unless we manually update the version manifest
# The version manifest is technically updated automatically by OM in remote mode, but this is faster.
@mark.e2e_om_remotemode
def test_update_om_version_manifest(ops_manager: MongoDBOpsManager):
    ops_manager.update_version_manifest()


@mark.e2e_om_remotemode
def test_replica_sets_reaches_running_phase(replica_set: MongoDB, replica_set_ent: MongoDB):
    """Doing this in parallel for faster success"""
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)
    replica_set_ent.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_om_remotemode
def test_replica_set_reaches_failed_phase(replica_set: MongoDB):
    replica_set.load()
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
