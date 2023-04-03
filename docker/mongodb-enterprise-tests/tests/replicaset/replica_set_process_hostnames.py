# This test currently relies on MetalLB IP assignment that is configured for kind in scripts/dev/recreate_kind_cluster.sh
# Each service of type LoadBalancer will get IP starting from 172.18.255.200
# scripts/dev/coredns_single_cluster.yaml configures that my-replica-set-0.mongodb.interconnected starts at 172.18.255.200

# Tests checking externalDomain (consider updating all of them when changing test logic here):
#   tls/tls_replica_set_process_hostnames.py
#   replicaset/replica_set_process_hostnames.py
#   om_ops_manager_backup_tls_custom_ca.py

import pytest
from pytest import fixture

from kubetester import (
    create_or_update,
    try_load,
)
from kubetester.kubetester import (
    fixture as yaml_fixture,
)
from kubetester.mongodb import MongoDB, Phase
from tests.conftest import (
    external_domain_fqdns,
    default_external_domain,
    update_coredns_hosts,
)


@fixture
def replica_set_name() -> str:
    return "my-replica-set"


@fixture
def replica_set_members() -> int:
    return 3


@fixture(scope="function")
def replica_set(
    namespace: str,
    replica_set_name: str,
    replica_set_members: int,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), replica_set_name, namespace)
    try_load(resource)

    resource["spec"]["members"] = replica_set_members
    resource["spec"]["externalAccess"] = {}
    resource["spec"]["externalAccess"]["externalDomain"] = default_external_domain()
    resource.set_version(custom_mdb_version)

    return resource


@pytest.mark.e2e_replica_set_process_hostnames
def test_update_coredns():
    hosts = [
        ("172.18.255.200", "my-replica-set-0.mongodb.interconnected"),
        ("172.18.255.201", "my-replica-set-1.mongodb.interconnected"),
        ("172.18.255.202", "my-replica-set-2.mongodb.interconnected"),
        ("172.18.255.203", "my-replica-set-3.mongodb.interconnected"),
    ]

    update_coredns_hosts(hosts)


@pytest.mark.e2e_replica_set_process_hostnames
def test_create_replica_set(replica_set: MongoDB):
    create_or_update(replica_set)


@pytest.mark.e2e_replica_set_process_hostnames
def test_replica_set_in_running_state(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=1000)


@pytest.mark.e2e_replica_set_process_hostnames
def test_replica_check_automation_config(replica_set: MongoDB):
    processes = replica_set.get_automation_config_tester().get_replica_set_processes(replica_set.name)
    hostnames = [process["hostname"] for process in processes]
    assert hostnames == external_domain_fqdns(replica_set.name, replica_set.get_members(), default_external_domain())


@pytest.mark.e2e_replica_set_process_hostnames
def test_connectivity(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_connectivity()
