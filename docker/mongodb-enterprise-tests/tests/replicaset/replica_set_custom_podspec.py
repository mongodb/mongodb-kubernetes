from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.custom_podspec import assert_stateful_set_podspec
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-custom-podspec.yaml"), namespace=namespace
    )
    return resource.create()


@mark.e2e_replica_set_custom_podspec
def test_replica_set_reaches_running_phase(replica_set):
    replica_set.assert_reaches_phase("Running", timeout=600)


@mark.e2e_replica_set_custom_podspec
def test_stateful_set_spec_updated(replica_set, namespace):
    appsv1 = KubernetesTester.clients("appsv1")
    sts = appsv1.read_namespaced_stateful_set(replica_set.name, namespace)
    assert_stateful_set_podspec(
        sts.spec.template.spec,
        weight=50,
        topology_key="mykey-rs",
        grace_period_seconds=30,
    )

    host_aliases = sts.spec.template.spec.host_aliases
    alias = host_aliases[0]

    assert len(host_aliases) == 1
    assert alias.ip == "1.2.3.4"
    assert alias.hostnames[0] == "hostname"
    assert len(sts.spec.template.metadata.annotations) == 2
    assert sts.spec.template.metadata.annotations["key1"] == "val1"
    assert sts.spec.template.metadata.annotations["certHash"] == ""  # added by operator
