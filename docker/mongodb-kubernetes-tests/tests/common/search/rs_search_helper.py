"""ReplicaSet helper functions for MongoDBSearch e2e tests."""

from kubetester.certs import create_tls_certs
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_issuer_ca_filepath

logger = test_logger.get_test_logger(__name__)


def rs_search_tester(
    name: str,
    namespace: str,
    username: str,
    password: str,
    use_ssl: bool = True,
) -> SearchTester:
    """SearchTester for an RS source addressed by name + namespace (no ``mdb`` object)."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    conn_str = (
        f"mongodb://{username}:{password}@"
        f"{name}-0.{name}-svc.{namespace}.svc.cluster.local:27017/"
        f"?replicaSet={name}"
    )
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


def rs_search_tester_for_member(
    name: str,
    namespace: str,
    member_index: int,
    username: str,
    password: str,
    use_ssl: bool = True,
    read_preference: str = "secondaryPreferred",
) -> SearchTester:
    """Name-addressed SearchTester pinned to one RS member via directConnection."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    conn_str = (
        f"mongodb://{username}:{password}@"
        f"{name}-{member_index}.{name}-svc.{namespace}.svc.cluster.local:27017/"
        f"?directConnection=true&readPreference={read_preference}&authSource=admin"
    )
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


def get_rs_search_tester(mdb: MongoDB, username: str, password: str, use_ssl: bool = False) -> SearchTester:
    return rs_search_tester(mdb.name, mdb.namespace, username, password, use_ssl=use_ssl)


def get_rs_search_tester_for_member(
    mdb: MongoDB,
    member_index: int,
    username: str,
    password: str,
    *,
    use_ssl: bool = True,
    read_preference: str = "secondaryPreferred",
) -> SearchTester:
    """SearchTester pinned to one RS member via directConnection."""
    return rs_search_tester_for_member(
        mdb.name, mdb.namespace, member_index, username, password, use_ssl=use_ssl, read_preference=read_preference
    )


def mc_rs_search_tester(
    name: str,
    namespace: str,
    username: str,
    password: str,
    *,
    use_ssl: bool = True,
) -> SearchTester:
    """SearchTester for a MongoDBMulti RS addressed by name + namespace (no ``mdbmulti`` object)."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    # seed host is the first pod on cluster 0.
    seed_host = f"{name}-0-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{seed_host}/?replicaSet={name}&authSource=admin"
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


def mc_rs_search_tester_for_cluster_member(
    name: str,
    namespace: str,
    cluster_index: int,
    member_index: int,
    username: str,
    password: str,
    *,
    use_ssl: bool = True,
    direct_connection: bool = True,
    read_preference: str = "secondaryPreferred",
) -> SearchTester:
    """Name-addressed SearchTester pinned to a specific ``(cluster_index, member_index)`` mongod.

    The pod host is ``{name}-{cluster_index}-{member_index}-svc`` — every
    cluster has its own 0-indexed StatefulSet so ``member_index`` is
    per-cluster, not global. ``directConnection=true`` disables RS topology
    discovery so the driver does not silently follow back to the primary —
    critical so a "direct to cluster N" tester stays on cluster N after a
    failover, preserving the per-cluster locality assertion.
    """
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    pod_host = f"{name}-{cluster_index}-{member_index}-svc.{namespace}.svc.cluster.local:27017"
    parts = ["authSource=admin"]
    if direct_connection:
        parts.append("directConnection=true")
    parts.append(f"readPreference={read_preference}")
    qs = "&".join(parts)
    conn_str = f"mongodb://{username}:{password}@{pod_host}/?{qs}"
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


def get_mc_rs_search_tester(
    mdb_mc: MongoDBMulti,
    username: str,
    password: str,
    *,
    use_ssl: bool = True,
) -> SearchTester:
    return mc_rs_search_tester(mdb_mc.name, mdb_mc.namespace, username, password, use_ssl=use_ssl)


def get_mc_rs_search_tester_for_cluster_member(
    mdb_mc: MongoDBMulti,
    cluster_index: int,
    member_index: int,
    username: str,
    password: str,
    *,
    use_ssl: bool = True,
    direct_connection: bool = True,
    read_preference: str = "secondaryPreferred",
) -> SearchTester:
    return mc_rs_search_tester_for_cluster_member(
        mdb_mc.name,
        mdb_mc.namespace,
        cluster_index,
        member_index,
        username,
        password,
        use_ssl=use_ssl,
        direct_connection=direct_connection,
        read_preference=read_preference,
    )


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

    proxy_svc = search_resource_names.proxy_service_name(mdbs_resource_name)

    # Server certificate — SAN for the proxy service (Envoy presents this cert)
    server_domains = [f"{proxy_svc}.{namespace}.svc.cluster.local"]
    if extra_domains:
        server_domains.extend(extra_domains)
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(mdbs_resource_name),
        replicas=1,
        service_name=proxy_svc,
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
        service_name=proxy_svc,
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
    proxy_svc = search_resource_names.proxy_service_name(mdbs_resource_name)

    additional_domains = [
        f"{mongot_svc}.{namespace}.svc.cluster.local",
        f"{proxy_svc}.{namespace}.svc.cluster.local",
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


# verify_rs_mongod_parameters has been consolidated into replicaset_search_helper.py
# Re-export for backward compatibility with existing imports
from tests.common.search.replicaset_search_helper import verify_rs_mongod_parameters  # noqa: F401
