"""ReplicaSet helper functions for MongoDBSearch e2e tests."""

import yaml
from kubetester.certs import create_tls_certs
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb import MongoDB
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_issuer_ca_filepath

logger = test_logger.get_test_logger(__name__)


def get_rs_search_tester(mdb: MongoDB, username: str, password: str, use_ssl: bool = False) -> SearchTester:
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_replicaset(mdb, username, password, use_ssl=use_ssl, ca_path=ca_path)


def create_rs_lb_certificates(
    namespace: str,
    issuer: str,
    mdbs_resource_name: str,
    tls_cert_prefix: str,
    extra_domains: list[str] | None = None,
):
    """Create TLS certificates for the managed load balancer (Envoy proxy) in RS topology."""
    logger.info("Creating managed LB certificates for RS...")

    lb_server_cert_name = search_resource_names.lb_server_cert_name(mdbs_resource_name, tls_cert_prefix)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(mdbs_resource_name, tls_cert_prefix)

    lb_svc = search_resource_names.lb_service_name(mdbs_resource_name)

    # Server certificate — SAN for the LB service
    server_domains = [f"{lb_svc}.{namespace}.svc.cluster.local"]
    if extra_domains:
        server_domains.extend(extra_domains)
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(mdbs_resource_name),
        replicas=1,
        service_name=lb_svc,
        additional_domains=server_domains,
        secret_name=lb_server_cert_name,
    )
    logger.info(f"LB server certificate created: {lb_server_cert_name}")

    # Client certificate — wildcard SAN
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(mdbs_resource_name)}-client",
        replicas=1,
        service_name=lb_svc,
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name=lb_client_cert_name,
    )
    logger.info(f"LB client certificate created: {lb_client_cert_name}")


def create_rs_search_tls_cert(
    namespace: str,
    issuer: str,
    mdbs_resource_name: str,
    tls_cert_prefix: str,
    extra_domains: list[str] | None = None,
):
    """Create mongot TLS certificate for RS topology."""
    secret_name = search_resource_names.mongot_tls_cert_name(mdbs_resource_name, tls_cert_prefix)
    mongot_svc = search_resource_names.mongot_service_name(mdbs_resource_name)
    lb_svc = search_resource_names.lb_service_name(mdbs_resource_name)

    additional_domains = [
        f"{mongot_svc}.{namespace}.svc.cluster.local",
        f"{lb_svc}.{namespace}.svc.cluster.local",
    ]
    if extra_domains:
        additional_domains.extend(extra_domains)

    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.mongot_statefulset_name(mdbs_resource_name),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    logger.info(f"RS Search TLS certificate created: {secret_name}")


def verify_rs_mongod_parameters(
    namespace: str,
    mdb_resource_name: str,
    member_count: int,
    expected_host: str,
):
    """Verify each RS member's mongod has correct mongotHost and searchIndexManagementHostAndPort."""

    def check_mongod_parameters():
        all_correct = True
        status_msgs = []

        for idx in range(member_count):
            pod_name = f"{mdb_resource_name}-{idx}"

            try:
                mongod_config = yaml.safe_load(
                    KubernetesTester.run_command_in_pod_container(
                        pod_name, namespace, ["cat", "/data/automation-mongod.conf"]
                    )
                )
                set_parameter = mongod_config.get("setParameter", {})
                mongot_host = set_parameter.get("mongotHost", "")
                search_index_host = set_parameter.get("searchIndexManagementHostAndPort", "")

                if mongot_host != expected_host:
                    all_correct = False
                    status_msgs.append(f"Pod {pod_name}: mongotHost={mongot_host}, expected={expected_host}")
                elif search_index_host != expected_host:
                    all_correct = False
                    status_msgs.append(f"Pod {pod_name}: searchIndexMgmt={search_index_host}, expected={expected_host}")
                else:
                    status_msgs.append(f"Pod {pod_name}: hosts correctly set to {expected_host}")

            except Exception as e:
                all_correct = False
                status_msgs.append(f"Pod {pod_name}: Error - {e}")

        return all_correct, "\n".join(status_msgs)

    run_periodically(check_mongod_parameters, timeout=300, sleep_time=10, msg="RS mongod search parameters")
    logger.info("All RS members have correct mongod search parameters")
