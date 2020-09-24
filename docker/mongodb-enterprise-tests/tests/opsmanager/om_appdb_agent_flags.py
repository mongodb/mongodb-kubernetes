from pytest import mark, fixture

from kubetester import find_fixture

from kubetester.opsmanager import MongoDBOpsManager
from kubetester.mongodb import Phase

from kubetester.kubetester import KubernetesTester

from typing import Optional


@fixture(scope="module")
def appdb(namespace: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        find_fixture("om_validation.yaml"), namespace=namespace
    )

    resource["spec"]["applicationDatabase"]["agent"] = {
        "startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}
    }
    resource["spec"]["applicationDatabase"]["version"] = "4.1.0"

    return resource.create()


@mark.e2e_om_appdb_agent_flags
def test_appdb(appdb: MongoDBOpsManager):
    appdb.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_om_appdb_agent_flags
def test_appdb_has_agent_flags(appdb: MongoDBOpsManager, namespace: str):
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"om-validate-db-{i}", namespace, cmd,
        )
        assert result != "0"
