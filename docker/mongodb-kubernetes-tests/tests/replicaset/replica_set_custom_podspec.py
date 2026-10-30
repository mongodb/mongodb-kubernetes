from kubetester import list_matching_pods, read_configmap, try_load, wait_for_pods_ready
from kubetester.custom_podspec import assert_stateful_set_podspec
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_static_containers
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-custom-podspec.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    try_load(resource)
    return resource


@skip_if_static_containers
@mark.e2e_replica_set_custom_podspec
def test_replica_set_reaches_running_phase(replica_set):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_static_containers
@mark.e2e_replica_set_custom_podspec
def test_stateful_set_spec_updated(replica_set, namespace):
    appsv1 = KubernetesTester.clients("appsv1")
    sts = appsv1.read_namespaced_stateful_set(replica_set.name, namespace)
    containers_spec = [
        {
            "name": "mongodb-enterprise-database",
            "resources": {
                "claims": None,
                "limits": {
                    "cpu": "2",
                },
                "requests": {
                    "cpu": "1",
                },
            },
            "volume_mounts": [
                {
                    "name": "database-scripts",
                    "mount_path": "/opt/scripts",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": True,
                    "recursive_read_only": None,
                },
                {
                    "name": "test-volume",
                    "mount_path": "/somewhere",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
                {
                    "name": "agent-api-key",
                    "mount_path": "/mongodb-automation/agent-api-key",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
                {
                    "name": "agent-settings",
                    "mount_path": "/etc/mongodb-agent",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": True,
                    "recursive_read_only": None,
                },
                {
                    "name": "data",
                    "mount_path": "/data",
                    "sub_path": "data",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
                {
                    "name": "data",
                    "mount_path": "/journal",
                    "sub_path": "journal",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
                {
                    "name": "data",
                    "mount_path": "/var/log/mongodb-mms-automation",
                    "sub_path": "logs",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
                {
                    "name": "agent",
                    "mount_path": "/tmp",
                    "sub_path": "tmp",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
                {
                    "name": "agent",
                    "mount_path": "/var/lib/mongodb-mms-automation",
                    "sub_path": "mongodb-mms-automation",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
                {
                    "name": "agent",
                    "mount_path": "/mongodb-automation",
                    "sub_path": "mongodb-automation",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                },
            ],
        },
        {
            "name": "side-car",
            "image": "busybox:latest",
            "volume_mounts": [
                {
                    "mount_path": "/somewhere",
                    "name": "test-volume",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                    "recursive_read_only": None,
                }
            ],
            "command": ["/bin/sh"],
            "args": ["-c", "echo ok > /somewhere/busybox_file && sleep 7200"],
        },
    ]

    assert_stateful_set_podspec(
        sts.spec.template.spec,
        weight=50,
        topology_key="mykey-rs",
        grace_period_seconds=30,
        containers_spec=containers_spec,
    )

    host_aliases = sts.spec.template.spec.host_aliases
    alias = host_aliases[0]

    assert len(host_aliases) == 1
    assert alias.ip == "1.2.3.4"
    assert alias.hostnames[0] == "hostname"
    # key1 is the user's annotation. The operator also sets the agent startup digest so a settings
    # or credential change rolls the pods.
    assert sts.spec.template.metadata.annotations["key1"] == "val1"
    assert len(sts.spec.template.metadata.annotations) == 2
    assert "mongodb.com/agent-startup-config-hash" in sts.spec.template.metadata.annotations


@skip_if_static_containers
@mark.e2e_replica_set_custom_podspec
def test_startup_option_change_replaces_all_pods(replica_set, namespace):
    replica_set.load()
    appsv1 = KubernetesTester.clients("appsv1")
    sts = appsv1.read_namespaced_stateful_set(replica_set.name, namespace)
    assert sts.spec.update_strategy.type == "RollingUpdate"

    replica_count = replica_set["spec"]["members"]
    old_pods = wait_for_pods_ready(namespace, name_prefix=replica_set.name, expected_count=replica_count, timeout=300)
    old_uids = {pod.metadata.name: pod.metadata.uid for pod in old_pods}
    expected_names = {f"{replica_set.name}-{i}" for i in range(replica_count)}
    assert set(old_uids) == expected_names
    old_hash = sts.spec.template.metadata.annotations["mongodb.com/agent-startup-config-hash"]

    replica_set["spec"]["agent"] = {"startupOptions": {"maxLogFileSize": "10485760"}}
    replica_set.update()

    def settings_hash_changed():
        current_sts = appsv1.read_namespaced_stateful_set(replica_set.name, namespace)
        current_hash = current_sts.spec.template.metadata.annotations.get("mongodb.com/agent-startup-config-hash")
        return current_hash is not None and current_hash != old_hash

    assert KubernetesTester.wait_until(
        settings_hash_changed, timeout=600, sleep_time=5, msg="agent startup settings hash updated"
    )
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    updated_sts = appsv1.read_namespaced_stateful_set(replica_set.name, namespace)
    new_hash = updated_sts.spec.template.metadata.annotations["mongodb.com/agent-startup-config-hash"]
    assert new_hash != old_hash

    def all_pods_replaced():
        pods = {pod.metadata.name: pod for pod in list_matching_pods(namespace, name_prefix=replica_set.name)}
        return all(name in pods and pods[name].metadata.uid != uid for name, uid in old_uids.items())

    assert KubernetesTester.wait_until(
        all_pods_replaced, timeout=600, sleep_time=5, msg="all StatefulSet pods replaced"
    )
    ready_pods = wait_for_pods_ready(namespace, name_prefix=replica_set.name, expected_count=replica_count, timeout=300)
    assert {pod.metadata.name for pod in ready_pods} == set(old_uids)

    settings = read_configmap(namespace, f"{replica_set.name}-agent-settings")
    assert "maxLogFileSize=10485760\n" in settings["automation-agent.conf"]
