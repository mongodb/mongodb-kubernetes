from kubetester import find_fixture, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.pod_logs import assert_log_types_in_structured_json_pod_log, get_all_default_log_types, get_all_log_types
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
)

default_download_base = "/var/lib/mongodb-mms-automation"
custom_download_base = "/var/lib/mongodb-mms-automation-custom"
brand_new_download_base = "/custom/custom-download-base"


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("sharded-cluster.yaml"), namespace=namespace)

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    resource["spec"]["configSrv"] = {
        "agent": {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFileSrv"}}
    }
    resource["spec"]["mongos"] = {
        "agent": {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFileMongos"}}
    }
    resource["spec"]["shard"] = {
        "agent": {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFileShard"}}
    }

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@mark.e2e_sharded_cluster_agent_flags
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_agent_flags
def test_create_sharded_cluster(sc: MongoDB):
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1000 if is_default_architecture_static() else 2000)


@mark.e2e_sharded_cluster_agent_flags
def test_default_download_base_in_automation_config(sc: MongoDB):
    options = sc.get_automation_config_tester().automation_config.get("options", {})
    assert options.get("downloadBase") == default_download_base


@mark.e2e_sharded_cluster_agent_flags
def test_sharded_cluster_has_agent_flags(sc: MongoDB):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        cluster_idx = cluster_member_client.cluster_index

        for member_idx in range(sc.shard_members_in_cluster(cluster_member_client.cluster_name)):
            cmd = [
                "/bin/sh",
                "-c",
                "ls /var/log/mongodb-mms-automation/customLogFileShard* | wc -l",
            ]
            result = KubernetesTester.run_command_in_pod_container(
                sc.shard_pod_name(0, member_idx, cluster_idx),
                sc.namespace,
                cmd,
                api_client=cluster_member_client.api_client,
            )
            assert result != "0"

        for member_idx in range(sc.config_srv_members_in_cluster(cluster_member_client.cluster_name)):
            cmd = [
                "/bin/sh",
                "-c",
                "ls /var/log/mongodb-mms-automation/customLogFileSrv* | wc -l",
            ]
            result = KubernetesTester.run_command_in_pod_container(
                sc.config_srv_pod_name(member_idx, cluster_idx),
                sc.namespace,
                cmd,
                api_client=cluster_member_client.api_client,
            )
            assert result != "0"

        for member_idx in range(sc.mongos_members_in_cluster(cluster_member_client.cluster_name)):
            cmd = [
                "/bin/sh",
                "-c",
                "ls /var/log/mongodb-mms-automation/customLogFileMongos* | wc -l",
            ]
            result = KubernetesTester.run_command_in_pod_container(
                sc.mongos_pod_name(member_idx, cluster_idx),
                sc.namespace,
                cmd,
                api_client=cluster_member_client.api_client,
            )
            assert result != "0"


@mark.e2e_sharded_cluster_agent_flags
def test_log_types_without_audit_enabled(sc: MongoDB):
    _assert_log_types_in_pods(sc, get_all_default_log_types())


@mark.e2e_sharded_cluster_agent_flags
def test_enable_audit_log(sc: MongoDB):
    additional_mongod_config = {
        "auditLog": {
            "destination": "file",
            "format": "JSON",
            "path": "/var/log/mongodb-mms-automation/mongodb-audit-changed.log",
        }
    }
    sc["spec"]["configSrv"]["additionalMongodConfig"] = additional_mongod_config
    sc["spec"]["mongos"]["additionalMongodConfig"] = additional_mongod_config
    sc["spec"]["shard"]["additionalMongodConfig"] = additional_mongod_config
    sc.update()

    sc.assert_reaches_phase(Phase.Running, timeout=2500)


@mark.e2e_sharded_cluster_agent_flags
def test_log_types_with_audit_enabled(sc: MongoDB):
    _assert_log_types_in_pods(sc, get_all_log_types())


@mark.e2e_sharded_cluster_agent_flags
def test_enable_scram_auth(sc: MongoDB):
    sc.load()
    sc["spec"]["security"] = sc["spec"].get("security", {})
    sc["spec"]["security"]["authentication"] = {
        "enabled": True,
        "modes": ["SCRAM"],
    }
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=2500 if is_default_architecture_static() else 3500)


@mark.e2e_sharded_cluster_agent_flags
def test_default_download_base_keyfile_in_automation_config(sc: MongoDB):
    expected_keyfile = f"{default_download_base}/keyfile"
    auth = sc.get_automation_config_tester().automation_config.get("auth", {})
    assert auth.get("keyfile") == expected_keyfile


@mark.e2e_sharded_cluster_agent_flags
def test_set_custom_download_base(sc: MongoDB):
    sc.load()
    sc["spec"]["downloadBase"] = custom_download_base
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=2500 if is_default_architecture_static() else 3500)


@mark.e2e_sharded_cluster_agent_flags
def test_custom_download_base_in_automation_config(sc: MongoDB):
    options = sc.get_automation_config_tester().automation_config.get("options", {})
    assert options.get("downloadBase") == custom_download_base


@mark.e2e_sharded_cluster_agent_flags
def test_custom_download_base_keyfile_in_automation_config(sc: MongoDB):
    sc.load()
    expected_keyfile = f"{sc['spec']['downloadBase']}/keyfile"
    auth = sc.get_automation_config_tester().automation_config.get("auth", {})
    assert auth.get("keyfile") == expected_keyfile


@mark.e2e_sharded_cluster_agent_flags
def test_set_brand_new_download_base(sc: MongoDB):
    sc.load()
    sc["spec"]["downloadBase"] = brand_new_download_base
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=2500 if is_default_architecture_static() else 3500)


@mark.e2e_sharded_cluster_agent_flags
def test_brand_new_download_base_in_automation_config(sc: MongoDB):
    options = sc.get_automation_config_tester().automation_config.get("options", {})
    assert options.get("downloadBase") == brand_new_download_base


@mark.e2e_sharded_cluster_agent_flags
def test_brand_new_download_base_keyfile_in_automation_config(sc: MongoDB):
    sc.load()
    expected_keyfile = f"{sc['spec']['downloadBase']}/keyfile"
    auth = sc.get_automation_config_tester().automation_config.get("auth", {})
    assert auth.get("keyfile") == expected_keyfile


def _assert_log_types_in_pods(sc: MongoDB, expected_log_types: set[str]):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        cluster_idx = cluster_member_client.cluster_index
        api_client = cluster_member_client.api_client

        for member_idx in range(sc.shard_members_in_cluster(cluster_member_client.cluster_name)):
            assert_log_types_in_structured_json_pod_log(
                sc.namespace, sc.shard_pod_name(0, member_idx, cluster_idx), expected_log_types, api_client=api_client
            )

        for member_idx in range(sc.config_srv_members_in_cluster(cluster_member_client.cluster_name)):
            assert_log_types_in_structured_json_pod_log(
                sc.namespace, sc.config_srv_pod_name(member_idx, cluster_idx), expected_log_types, api_client=api_client
            )

        for member_idx in range(sc.mongos_members_in_cluster(cluster_member_client.cluster_name)):
            assert_log_types_in_structured_json_pod_log(
                sc.namespace, sc.mongos_pod_name(member_idx, cluster_idx), expected_log_types, api_client=api_client
            )
