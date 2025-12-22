# This test currently relies on MetalLB IP assignment that is configured for kind in scripts/dev/recreate_kind_cluster.sh
# Each service of type LoadBalancer will get IP starting from 172.18.255.200
# scripts/dev/coredns_single_cluster.yaml configures that my-replica-set-0.mongodb.interconnected starts at 172.18.255.200

# Other e2e tests checking externalDomain (consider updating all of them when changing test logic here):
#   tls/tls_replica_set_process_hostnames.py
#   replicaset/replica_set_process_hostnames.py
#   om_ops_manager_backup_tls_custom_ca.py


from kubetester import try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import assert_statefulset_architecture
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_default_architecture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import default_external_domain, external_domain_fqdns, update_coredns_hosts


@fixture(scope="module")
def replica_set_name() -> str:
    return "my-replica-set"


@fixture(scope="module")
def replica_set_members() -> int:
    return 3


@fixture(scope="module")
def server_certs(issuer: str, namespace: str, replica_set_members: int, replica_set_name: str):
    """
    Issues certificate containing only custom_service_fqdns in SANs
    """
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        replica_set_name,
        f"{replica_set_name}-cert",
        process_hostnames=external_domain_fqdns(replica_set_name, replica_set_members),
    )


@fixture(scope="function")
def replica_set(
    namespace: str,
    replica_set_name: str,
    replica_set_members: int,
    custom_mdb_version: str,
    server_certs: str,
    issuer_ca_configmap: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("test-tls-base-rs.yaml"), replica_set_name, namespace)
    try_load(resource)

    resource["spec"]["members"] = replica_set_members
    resource["spec"]["externalAccess"] = {}
    resource["spec"]["externalAccess"]["externalDomain"] = default_external_domain()
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    resource.set_version(custom_mdb_version)
    resource.set_architecture_annotation()

    return resource


@mark.e2e_replica_set_tls_process_hostnames
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_replica_set_tls_process_hostnames
def test_update_coredns():
    hosts = [
        ("172.18.255.200", "my-replica-set-0.mongodb.interconnected"),
        ("172.18.255.201", "my-replica-set-1.mongodb.interconnected"),
        ("172.18.255.202", "my-replica-set-2.mongodb.interconnected"),
        ("172.18.255.203", "my-replica-set-3.mongodb.interconnected"),
    ]

    update_coredns_hosts(hosts)


@mark.e2e_replica_set_tls_process_hostnames
def test_create_replica_set(replica_set: MongoDB):
    replica_set.update()


@mark.e2e_replica_set_tls_process_hostnames
def test_replica_set_in_running_state(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=1000)


@mark.e2e_replica_set_tls_process_hostnames
def test_automation_config_contains_external_domains_in_hostnames(replica_set: MongoDB):
    processes = replica_set.get_automation_config_tester().get_replica_set_processes(replica_set.name)
    hostnames = [process["hostname"] for process in processes]
    assert hostnames == external_domain_fqdns(replica_set.name, replica_set.get_members())


@mark.e2e_replica_set_tls_process_hostnames
def test_connectivity(replica_set: MongoDB, ca_path: str):
    tester = replica_set.tester(ca_path=ca_path)
    tester.assert_connectivity()


@mark.e2e_replica_set_tls_process_hostnames
def test_migrate_architecture(replica_set: MongoDB):
    """
    If the E2E is running with default architecture as non-static,
    then the test will migrate to static and vice versa.
    """
    original_default_architecture = get_default_architecture()
    target_architecture = "non-static" if original_default_architecture == "static" else "static"

    replica_set.trigger_architecture_migration()

    replica_set.load()
    assert replica_set["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

    replica_set.assert_abandons_phase(Phase.Running, timeout=1000)
    replica_set.assert_reaches_phase(Phase.Running, timeout=1000)

    # Read StatefulSet after successful reconciliation
    sts = replica_set.read_statefulset()
    assert_statefulset_architecture(sts, target_architecture)


@mark.e2e_replica_set_tls_process_hostnames
def test_db_connectable_after_architecture_change(replica_set: MongoDB, ca_path: str):
    tester = replica_set.tester(ca_path=ca_path)
    tester.assert_connectivity()
