import subprocess

from pytest import fixture, mark

from kubernetes.stream import stream

from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.custom_podspec import assert_stateful_set_podspec


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-custom-podspec.yaml"), namespace=namespace
    )
    resource.set_version(custom_mdb_version)
    yield resource.create()
    resource.delete()


@mark.e2e_replica_set_custom_podspec
def test_replica_set_reaches_running_phase(replica_set):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replica_set_custom_podspec
def test_stateful_set_spec_updated(replica_set, namespace):
    appsv1 = KubernetesTester.clients("appsv1")
    sts = appsv1.read_namespaced_stateful_set(replica_set.name, namespace)
    containers_spec = [
        {
            "name": "mongodb-enterprise-database",
            "resources": {"limits": {"cpu": "2",}, "requests": {"cpu": "1",},},
            "volume_mounts": [
                {
                    "name": "test-volume",
                    "mount_path": "/somewhere",
                    "sub_path": None,
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                },
                {
                    "name": "data",
                    "mount_path": "/data",
                    "sub_path": "data",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                },
                {
                    "name": "data",
                    "mount_path": "/journal",
                    "sub_path": "journal",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
                },
                {
                    "name": "data",
                    "mount_path": "/var/log/mongodb-mms-automation",
                    "sub_path": "logs",
                    "sub_path_expr": None,
                    "mount_propagation": None,
                    "read_only": None,
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
                }
            ],
            "command": ["/bin/sh"],
            "args": ["-c", "echo ok > /somewhere/busybox_file && sleep infinity"],
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
    assert len(sts.spec.template.metadata.annotations) == 2
    assert sts.spec.template.metadata.annotations["key1"] == "val1"
    assert sts.spec.template.metadata.annotations["certHash"] == ""  # added by operator
