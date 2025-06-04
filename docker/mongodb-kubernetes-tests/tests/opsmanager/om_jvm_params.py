import re
from typing import Optional

from dateutil.parser import parse
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import assert_log_rotation_process, is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

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
    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

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
        ops_manager.update()
        # Backup is not fully configured so we wait until Pending phase
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            timeout=900,
            msg_regexp="Oplog Store configuration is required for backup.*",
        )

    def test_om_jvm_params_configured(self, ops_manager: MongoDBOpsManager):
        for api_client, pod in ops_manager.read_om_pods():
            cmd = ["/bin/sh", "-c", "cat " + OM_CONF_PATH_DIR]

            result = KubernetesTester.run_command_in_pod_container(
                pod.metadata.name,
                ops_manager.namespace,
                cmd,
                container="mongodb-ops-manager",
                api_client=api_client,
            )
            java_params = self.parse_java_params(result, JAVA_MMS_UI_OPTS)
            assert "-Xmx4291m" in java_params
            assert "-Xms343m" in java_params

    def test_om_process_mem_scales(self, ops_manager: MongoDBOpsManager):
        for api_client, pod in ops_manager.read_om_pods():

            cmd = ["/bin/sh", "-c", "ps aux"]
            result = KubernetesTester.run_command_in_pod_container(
                pod.metadata.name,
                ops_manager.namespace,
                cmd,
                container="mongodb-ops-manager",
                api_client=api_client,
            )
            rss = self.parse_rss(result)

            # rss is in kb, we want to ensure that it is > 400mb
            # this is to ensure that OM can grow properly with it's container
            assert int(rss) / 1024 > 400

    def test_om_jvm_backup_params_configured(self, ops_manager: MongoDBOpsManager):
        for api_client, pod in ops_manager.read_backup_pods():

            cmd = ["/bin/sh", "-c", "cat " + OM_CONF_PATH_DIR]

            result = KubernetesTester.run_command_in_pod_container(
                pod.metadata.name,
                ops_manager.namespace,
                cmd,
                container="mongodb-backup-daemon",
                api_client=api_client,
            )

            java_params = self.parse_java_params(result, JAVA_DAEMON_OPTS)
            assert "-Xmx4352m" in java_params
            assert "-Xms4352m" in java_params

    def test_om_log_rotate_configured(self, ops_manager: MongoDBOpsManager):
        processes = ops_manager.get_automation_config_tester().automation_config["processes"]
        expected = {
            "timeThresholdHrs": 1,
            "numUncompressed": 2,
            "numTotal": 10,
            "sizeThresholdMB": 0.0001,
            "percentOfDiskspace": 10,
        }
        for p in processes:
            assert p["logRotate"] == expected

    def test_update_appdb_log_rotation_keep_deprecated_fields(self, ops_manager):
        # configuration over mongod takes precedence over deprecated logRotation directly under agent
        ops_manager["spec"]["applicationDatabase"]["agent"]["mongod"] = {
            "logRotate": {
                "sizeThresholdMB": "1",
                "percentOfDiskspace": "1",
                "numTotal": 1,
                "timeThresholdHrs": 1,
                "numUncompressed": 1,
            }
        }

        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            timeout=900,
            msg_regexp="Oplog Store configuration is required for backup.*",
        )

    def test_om_log_rotate_has_changed(self, ops_manager: MongoDBOpsManager):
        processes = ops_manager.get_automation_config_tester().automation_config["processes"]
        expected = {
            "timeThresholdHrs": 1,
            "numUncompressed": 1,
            "numTotal": 1,
            "sizeThresholdMB": 1,
            "percentOfDiskspace": 1,
        }
        for p in processes:
            assert p["logRotate"] == expected

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
