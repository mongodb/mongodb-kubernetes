import os
from typing import Optional

from kubetester import create_or_update_secret, get_service
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.common.placeholders import placeholders
from tests.conftest import (
    create_appdb_certs,
    default_external_domain,
    external_domain_fqdns,
    update_coredns_hosts,
)

OM_NAME = "om-appdb-external"
APPDB_NAME = f"{OM_NAME}-db"
APPDB_EXTERNAL_DOMAINS = external_domain_fqdns(APPDB_NAME, 3)


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, OM_NAME)


@fixture(scope="module")
def appdb_certs(namespace: str, issuer: str):
    return create_appdb_certs(namespace, issuer, APPDB_NAME, additional_domains=APPDB_EXTERNAL_DOMAINS)


@fixture(scope="module")
@mark.usefixtures("appdb_certs", "ops_manager_certs", "issuer_ca_configmap")
def ops_manager(
    namespace: str,
    issuer_ca_configmap: str,
    appdb_certs: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    issuer_ca_filepath: str,
) -> MongoDBOpsManager:
    create_or_update_secret(namespace, "appdb-secret", {"password": "Hello-World!"})

    print("Creating OM object")
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_monitoring_tls.yaml"), name=OM_NAME, namespace=namespace
    )
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    om["spec"]["applicationDatabase"]["externalAccess"] = {
        "externalDomain": default_external_domain(),
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": False,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                    {
                        "name": "testing2",
                        "port": 27019,
                    },
                ],
            },
        },
    }

    # ensure the requests library will use this CA when communicating with Ops Manager
    os.environ["REQUESTS_CA_BUNDLE"] = issuer_ca_filepath

    return om


@mark.e2e_om_appdb_external_connectivity
def test_configure_dns():
    host_mappings = [
        (
            "172.18.255.200",
            APPDB_EXTERNAL_DOMAINS[0],
        ),
        (
            "172.18.255.201",
            APPDB_EXTERNAL_DOMAINS[1],
        ),
        (
            "172.18.255.202",
            APPDB_EXTERNAL_DOMAINS[2],
        ),
    ]

    update_coredns_hosts(
        host_mappings=host_mappings,
    )


@mark.e2e_om_appdb_external_connectivity
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.assert_appdb_preferred_hostnames_are_added()
    ops_manager.assert_appdb_hostnames_are_correct()


@mark.e2e_om_appdb_external_connectivity
def test_appdb_group_is_monitored(ops_manager: MongoDBOpsManager):
    ops_manager.assert_appdb_monitoring_group_was_created()
    ops_manager.assert_monitoring_data_exists()


@mark.e2e_om_appdb_external_connectivity
def test_service_exists(namespace: str):
    for i in range(3):
        service = get_service(
            namespace,
            f"{APPDB_NAME}-{i}-svc-external",
        )
        assert service.spec.type == "LoadBalancer"
        assert service.spec.ports[0].port == 27017
        assert service.spec.ports[1].port == 27018
        assert service.spec.ports[2].port == 27019


@mark.e2e_om_appdb_external_connectivity
def test_placeholders_in_external_services(ops_manager: MongoDBOpsManager, namespace: str):
    ops_manager.load()

    ops_manager["spec"]["applicationDatabase"]["externalAccess"]["externalService"][
        "annotations"
    ] = placeholders.get_annotations_with_placeholders_for_single_cluster()
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=300)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)

    for pod_idx in range(3):
        service = get_service(namespace, f"{APPDB_NAME}-{pod_idx}-svc-external")
        assert (
            service.metadata.annotations
            == placeholders.get_expected_annotations_single_cluster_with_external_domain(
                APPDB_NAME, namespace, pod_idx, default_external_domain()
            )
        )
