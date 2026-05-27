"""ReplicaSet helper functions for MongoDBSearch e2e tests."""

from kubetester.certs import create_tls_certs
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_issuer_ca_filepath

logger = test_logger.get_test_logger(__name__)


def get_rs_search_tester(mdb: MongoDB, username: str, password: str, use_ssl: bool = False) -> SearchTester:
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_replicaset(mdb, username, password, use_ssl=use_ssl, ca_path=ca_path)


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
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    conn_str = (
        f"mongodb://{username}:{password}@"
        f"{mdb.name}-{member_index}.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017/"
        f"?directConnection=true&readPreference={read_preference}&authSource=admin"
    )
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


# ---------------------------------------------------------------------------
# MongoDBMulti (MC) RS testers — used by the multi-cluster search e2e suite.
#
# Same-cluster-mongot routing invariant: each cluster's mongods are AC-patched
# with their own cluster's envoy proxy-svc as ``mongotHost``. Consequently,
# any ``$search`` / ``$vectorSearch`` aggregation executed against a mongod
# in cluster N is served by cluster N's mongot — never any other cluster's.
# A failure in cluster N is therefore observable only via a tester
# direct-connected to a mongod in cluster N.
#
# Two flavors of MC tester live below:
#
# * ``get_mc_rs_search_tester`` — mesh-DNS, RS-aware. Seeds from cluster-0
#   member-0 and uses ``?replicaSet=...`` so the pymongo driver discovers and
#   follows the primary. Use for happy-path tests where the cluster identity
#   of the served query does not matter.
#
# * ``get_mc_rs_search_tester_for_cluster_member`` — directConnection to a
#   specific (cluster_index, member_index) mongod. Use for per-cluster
#   locality assertions: a tester pinned to cluster N's mongod always lands
#   on cluster N's mongot, regardless of which cluster currently holds the
#   RS primary.
# ---------------------------------------------------------------------------


def get_mc_rs_search_tester(
    mdb_mc: MongoDBMulti,
    username: str,
    password: str,
    *,
    use_ssl: bool = True,
) -> SearchTester:
    """Mesh-DNS RS-aware SearchTester against a MongoDBMulti source.

    Seeds from cluster-0/member-0 and uses ``?replicaSet=<name>`` so the
    pymongo driver discovers the topology and follows the primary across
    failovers. Mirrors the inline helper in ``q2_mc_rs_steady`` so per-test
    happy-path search calls share one connection-string template.
    """
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    seed_host = f"{mdb_mc.name}-0-0-svc.{mdb_mc.namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{seed_host}/" f"?replicaSet={mdb_mc.name}&authSource=admin"
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


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
    """SearchTester pinned to a specific ``(cluster_index, member_index)`` mongod.

    The pod host is ``{name}-{cluster_index}-{member_index}-svc`` — every
    cluster has its own 0-indexed StatefulSet so ``member_index`` is
    per-cluster, not global.

    ``directConnection=true`` disables RS topology discovery so the driver
    does not silently follow back to the primary. Critical for per-cluster
    mongot tests — without it, after a primary failover the "direct to
    cluster N" tester would silently route to whichever cluster currently
    holds the primary, defeating the locality assertion. ``$search`` /
    ``$vectorSearch`` are read aggregations any mongod can serve;
    ``readPreference=secondaryPreferred`` lets the pinned pod handle the
    query whether it is currently primary or secondary.

    Lifts ``q2_mc_rs_steady._direct_search_tester_for_cluster``, lifting
    its hardcoded ``member_index=0`` into a parameter.
    """
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    pod_host = f"{mdb_mc.name}-{cluster_index}-{member_index}-svc" f".{mdb_mc.namespace}.svc.cluster.local:27017"
    parts = ["authSource=admin"]
    if direct_connection:
        parts.append("directConnection=true")
    parts.append(f"readPreference={read_preference}")
    qs = "&".join(parts)
    conn_str = f"mongodb://{username}:{password}@{pod_host}/?{qs}"
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


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
