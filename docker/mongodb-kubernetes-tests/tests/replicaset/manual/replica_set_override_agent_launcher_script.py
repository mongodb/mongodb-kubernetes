import base64

from kubetester import find_fixture, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture

# This test is intended for manual run only.
#
# It's for quick iteration on changes to agent-launcher.sh.
# It's deploying a replica set with 1 member with the local copy of agent-launcher.sh and agent-launcher-lib.sh scripts
# from docker/mongodb-kubernetes-init-database/content.
# Scripts are injected (mounted) into their standard location in init image and scripts from init-database image are overwritten.
#
# Thanks to this, it is possible to quickly iterate on the script without the need to build and push init-database image.


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), namespace=namespace
    )

    resource["spec"]["topology"] = "SingleCluster"
    resource["spec"]["applicationDatabase"]["topology"] = "SingleCluster"
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if try_load(resource):
        return resource

    resource.update()
    return resource


@fixture(scope="function")
def replica_set(ops_manager: str, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("replica-set-override-agent-launcher-script.yaml"),
        namespace=namespace,
    ).configure(ops_manager, "replica-set")
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["logLevel"] = "INFO"
    resource["spec"]["additionalMongodConfig"] = {
        "auditLog": {
            "destination": "file",
            "format": "JSON",
            "path": "/var/log/mongodb-mms-automation/mongodb-audit-changed.log",
        },
    }
    resource["spec"]["agent"] = {
        "startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFileWithoutExt"}
    }

    return resource


def test_replica_set(replica_set: MongoDB):
    with open("../mongodb-kubernetes-init-database/content/agent-launcher.sh", "rb") as f:
        agent_launcher = base64.b64encode(f.read()).decode("utf-8")

    with open("../mongodb-kubernetes-init-database/content/agent-launcher-lib.sh", "rb") as f:
        agent_launcher_lib = base64.b64encode(f.read()).decode("utf-8")

    command = f"""
echo -n "{agent_launcher}" | base64 -d > /opt/scripts/agent-launcher.sh
echo -n "{agent_launcher_lib}" | base64 -d > /opt/scripts/agent-launcher-lib.sh
    """

    replica_set["spec"]["podSpec"]["podTemplate"]["spec"]["initContainers"][0]["args"] = ["-c", command]

    replica_set.update()


def test_om_running(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running)


def test_replica_set_running(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running)
