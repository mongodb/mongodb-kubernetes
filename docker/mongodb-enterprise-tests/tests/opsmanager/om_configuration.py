from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase, KubernetesTester
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
import re
from tests.opsmanager.om_base import OpsManagerBase

OM_CONF_PATH_DIR = "mongodb-ops-manager/conf/mms.conf"
JAVA_MMS_UI_OPTS = "JAVA_MMS_UI_OPTS"
JAVA_DAEMON_OPTS = "JAVA_DAEMON_OPTS"


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    """ The fixture for Ops Manager to be created."""
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_jvm_params.yaml"), namespace=namespace
    )
    return om.create()


@mark.e2e_om_jvm_params
class TestOpsManagerCreationWithJvmParams(OpsManagerBase):
    def test_om_created(self, ops_manager: MongoDBOpsManager):
        ops_manager.assert_reaches_phase(Phase.Running, timeout=900)

    def test_om_jvm_params_configured(self, ops_manager: MongoDBOpsManager):
        pod_name = ops_manager["metadata"]["name"] + "-0"
        cmd = ["/bin/sh", "-c", "cat " + OM_CONF_PATH_DIR]

        result = self.run_command_in_pod_container(pod_name=pod_name, cmd=cmd)
        java_params = self.parse_java_params(result, JAVA_MMS_UI_OPTS)
        assert "-Xmx4291m" in java_params
        assert "-Xms343m" in java_params

    def test_om_process_mem_scales(self, ops_manager: MongoDBOpsManager):
        pod_name = ops_manager["metadata"]["name"] + "-0"
        cmd = ["/bin/sh", "-c", "ps aux"]
        result = self.run_command_in_pod_container(pod_name=pod_name, cmd=cmd)
        rss = self.parse_rss(result)

        # rss is in kb, we want to ensure that it is > 400mb
        # this is to ensure that OM can grow properly with it's container
        assert int(rss) / 1024 > 400

    def test_om_jvm_backup_params_configured(self, ops_manager: MongoDBOpsManager):
        pod_name = ops_manager["metadata"]["name"] + "-backup-daemon-0"
        cmd = ["/bin/sh", "-c", "cat " + OM_CONF_PATH_DIR]

        result = self.run_command_in_pod_container(pod_name=pod_name, cmd=cmd)

        java_params = self.parse_java_params(result, JAVA_DAEMON_OPTS)
        assert "-Xmx4352m" in java_params
        assert "-Xms4352m" in java_params

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
