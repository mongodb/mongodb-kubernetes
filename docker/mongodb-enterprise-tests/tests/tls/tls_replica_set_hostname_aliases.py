from typing import List

import kubernetes.client
import pytest
from pytest import fixture

from kubetester import (
    create_or_update, try_load, create_or_update_service,
)
from kubetester.kubetester import (
    fixture as yaml_fixture,
)
from kubetester.mongodb import MongoDB, Phase

from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_agent_tls_certs,
)


@fixture(scope="module")
def replica_set_name() -> str:
    return "my-replica-set"


@fixture(scope="module")
def replica_set_members() -> int:
    return 3


@fixture(scope="module")
def custom_service_names(replica_set_name: str, replica_set_members: int) -> List[str]:
    return [f"{replica_set_name}-{i}" for i in range(0, replica_set_members)]


@fixture(scope="module")
def custom_service_fqdns(namespace: str, custom_service_names: List[str]) -> List[str]:
    return [f"{service_name}.{namespace}.svc.cluster.local" for service_name in custom_service_names]


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str, replica_set_name: str, custom_service_fqdns: List[str]):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        replica_set_name,
        f"{replica_set_name}-cert",
        hostname_aliases=custom_service_fqdns
    )


@fixture(scope="function")
def replica_set(namespace: str, replica_set_name: str, replica_set_members: int, custom_service_fqdns: List[str], custom_mdb_version: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("test-tls-base-rs.yaml"), replica_set_name, namespace
    )
    try_load(resource)

    resource["spec"]["members"] = replica_set_members
    resource["spec"]["externalAccess"] = {
        "externalDomain": f"{namespace}.svc.cluster.local"
    }
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    resource.set_version(custom_mdb_version)

    return resource


@pytest.mark.e2e_replica_set_tls_process_hostnames
def test_create_additional_services(namespace: str, custom_service_names: List[str], replica_set: MongoDB):
    for i in range(0, replica_set["spec"]["members"]):
        create_or_update_service(namespace, custom_service_names[i], spec=kubernetes.client.V1ServiceSpec(
            type="ClusterIP",
            ports=[
                {
                    "port": 27017,
                    "targetPort": 27017,
                    "protocol": "TCP",
                }
            ],
            selector={
                "app": f"{replica_set.name}-svc",
                "statefulset.kubernetes.io/pod-name": f"{replica_set.name}-{i}",
            },
            publish_not_ready_addresses=True
        ))


@pytest.mark.e2e_replica_set_tls_process_hostnames
def test_create_replica_set(replica_set: MongoDB):
    create_or_update(replica_set)


@pytest.mark.e2e_replica_set_tls_process_hostnames
def test_replica_set_in_running_state(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=1000)


@pytest.mark.e2e_replica_set_tls_process_hostnames
def test_replica_check_automation_config(replica_set: MongoDB, custom_service_fqdns: List[str]):
    processes = replica_set.get_automation_config_tester().get_replica_set_processes(replica_set.name)
    hostnames = [process["hostname"] for process in processes]
    assert hostnames == custom_service_fqdns
