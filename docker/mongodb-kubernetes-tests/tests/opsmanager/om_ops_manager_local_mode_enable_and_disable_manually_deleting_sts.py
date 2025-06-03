from typing import Optional

from kubetester import delete_pod, delete_statefulset, get_pod_when_ready
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_static_containers
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_member_cluster_api_client, is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )

    resource["spec"]["replicas"] = 2
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource.update()


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
    ).configure(ops_manager)
    resource.set_version(custom_mdb_version)

    resource.update()
    return resource


@skip_if_static_containers
@mark.e2e_om_ops_manager_enable_local_mode_running_om
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1000)


@skip_if_static_containers
@mark.e2e_om_ops_manager_enable_local_mode_running_om
def test_enable_local_mode(ops_manager: MongoDBOpsManager, namespace: str):

    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_localmode-multiple-pv.yaml"), namespace=namespace)

    for member_cluster_name, sts_name in ops_manager.get_om_sts_names_in_member_clusters():
        # We manually delete the ops manager sts, it won't delete the pods as
        # the function by default does cascade=false
        delete_statefulset(namespace, sts_name, api_client=get_member_cluster_api_client(member_cluster_name))

    ops_manager.load()
    ops_manager["spec"]["configuration"] = {"automation.versions.source": "local"}
    ops_manager["spec"]["statefulSet"] = om["spec"]["statefulSet"]
    ops_manager.update()

    # At this point the operator has created a new sts but the existing pods can't be bound to
    # it because podspecs are immutable so the volumes field can't be changed
    # and thus we can't rollout

    for member_cluster_name, pod_name in ops_manager.get_om_pod_names_in_member_clusters():
        # So we manually delete one, wait for it to be ready
        # and do the same for the second one
        delete_pod(namespace, pod_name, api_client=get_member_cluster_api_client(member_cluster_name))
        get_pod_when_ready(namespace, f"statefulset.kubernetes.io/pod-name={pod_name}")

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@skip_if_static_containers
@mark.e2e_om_ops_manager_enable_local_mode_running_om
def test_add_mongodb_distros(ops_manager: MongoDBOpsManager, custom_mdb_version: str):
    ops_manager.download_mongodb_binaries(custom_mdb_version)


@skip_if_static_containers
@mark.e2e_om_ops_manager_enable_local_mode_running_om
def test_new_binaries_are_present(ops_manager: MongoDBOpsManager, namespace: str):
    cmd = [
        "/bin/sh",
        "-c",
        "ls /mongodb-ops-manager/mongodb-releases/*.tgz",
    ]
    for member_cluster_name, pod_name in ops_manager.get_om_pod_names_in_member_clusters():
        result = KubernetesTester.run_command_in_pod_container(
            pod_name,
            namespace,
            cmd,
            container="mongodb-ops-manager",
            api_client=get_member_cluster_api_client(member_cluster_name),
        )
        assert result != "0"


@skip_if_static_containers
@mark.e2e_om_ops_manager_enable_local_mode_running_om
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)
