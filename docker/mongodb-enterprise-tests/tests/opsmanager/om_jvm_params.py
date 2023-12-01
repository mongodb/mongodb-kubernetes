import re
from typing import Optional

from dateutil.parser import parse
from kubetester import create_or_update, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import (
    enable_appdb_multi_cluster_deployment,
)

OM_CONF_PATH_DIR = "mongodb-ops-manager/conf/mms.conf"
APPDB_LOG_DIR = "/data"
JAVA_MMS_UI_OPTS = "JAVA_MMS_UI_OPTS"
JAVA_DAEMON_OPTS = "JAVA_DAEMON_OPTS"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    """The fixture for Ops Manager to be created."""
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_jvm_params.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om["spec"]["applicationDatabase"]["agent"] = {
        "logRotate": {
            "sizeThresholdMB": "0.0001",
            "percentOfDiskspace": "10",
            "numTotal": 10,
            "timeThresholdHrs": 1,
            "numUncompressed": 2,
        },
        "systemLog": {
            "destination": "file",
            "path": APPDB_LOG_DIR + "/mongodb.log",
            "logAppend": False,
        },
    }

    return om

    if is_multi_cluster():
        enable_appdb_multi_cluster_deployment(om)

    try_load(om)
    return om


def is_date(file_name) -> bool:
    try:
        parse(file_name, fuzzy=True)
        return True

    except ValueError:
        return False


@mark.e2e_om_jvm_params
class TestOpsManagerCreationWithJvmParams:
    def test_om_created(self, ops_manager: MongoDBOpsManager):
        create_or_update(ops_manager)
        # Backup is not fully configured so we wait until Pending phase
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            timeout=900,
            msg_regexp="Oplog Store configuration is required for backup.*",
        )

    def test_om_jvm_params_configured(self, ops_manager: MongoDBOpsManager):
        pod_name = ops_manager.read_om_pods()[0].metadata.name
        cmd = ["/bin/sh", "-c", "cat " + OM_CONF_PATH_DIR]

        result = KubernetesTester.run_command_in_pod_container(
            pod_name, ops_manager.namespace, cmd, container="mongodb-ops-manager"
        )
        java_params = self.parse_java_params(result, JAVA_MMS_UI_OPTS)
        assert "-Xmx4291m" in java_params
        assert "-Xms343m" in java_params

    def test_om_process_mem_scales(self, ops_manager: MongoDBOpsManager):
        pod_name = ops_manager.read_om_pods()[0].metadata.name
        cmd = ["/bin/sh", "-c", "ps aux"]
        result = KubernetesTester.run_command_in_pod_container(
            pod_name, ops_manager.namespace, cmd, container="mongodb-ops-manager"
        )
        rss = self.parse_rss(result)

        # rss is in kb, we want to ensure that it is > 400mb
        # this is to ensure that OM can grow properly with it's container
        assert int(rss) / 1024 > 400

    def test_om_jvm_backup_params_configured(self, ops_manager: MongoDBOpsManager):
        pod_names = ops_manager.backup_daemon_pods_names()
        assert len(pod_names) == 1
        pod_name = pod_names[0]
        cmd = ["/bin/sh", "-c", "cat " + OM_CONF_PATH_DIR]

        result = KubernetesTester.run_command_in_pod_container(
            pod_name, ops_manager.namespace, cmd, container="mongodb-backup-daemon"
        )

        java_params = self.parse_java_params(result, JAVA_DAEMON_OPTS)
        assert "-Xmx4352m" in java_params
        assert "-Xms4352m" in java_params

    def test_om_log_rotate_configured(self, ops_manager: MongoDBOpsManager):
        pod_name = ops_manager.read_appdb_pods()[0][1].metadata.name
        cmd = ["/bin/sh", "-c", "ls " + APPDB_LOG_DIR]

        result = KubernetesTester.run_command_in_pod_container(
            pod_name, ops_manager.namespace, cmd, container="mongodb-agent"
        )

        found = False
        for row in result.split("\n"):
            if "mongodb.log" in row and is_date(row):
                found = True
                break

        assert found

    def parse_java_params(self, conf: str, opts_key: str) -> str:
        java_params = ""
        for line in conf.split("\n"):
            if not line.startswith("#") and opts_key in line:
                param_line = line.split("=", 1)
                assert len(param_line) != 0, "Expected key=value format"
                java_params = param_line[1]

        return java_params

    def parse_rss(self, ps_output: str) -> int:
        rss = 0
        sep = re.compile("[\s]+")

        for row in ps_output.split("\n"):
            columns = sep.split(row)
            if "ops-manager" in columns[10]:
                # RSS
                rss = int(columns[5])
                break

        return rss
