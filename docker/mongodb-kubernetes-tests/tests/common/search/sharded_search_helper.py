from kubetester import create_or_update_configmap, create_or_update_secret
from kubetester.certs import create_tls_certs
from kubetester.mongodb import MongoDB
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_issuer_ca_filepath

logger = test_logger.get_test_logger(__name__)


def create_per_shard_search_tls_certs(
    namespace: str,
    issuer: str,
    prefix: str,
    shard_count: int,
    mdb_resource_name: str,
    mdbs_resource_name: str,
):
    """
        Create per-shard TLS certificates for MongoDBSearch resource.

        For each shard, creates a certificate with DNS names for:
        - The mongot service: {search-name}-search-0-{shardName}-svc.{namespace}.svc.cluster.local
        - The proxy service: {search-name}-search-0-{shardName}-proxy-svc.{namespace}.svc.cluster.local

    a    Secret naming: search_resource_names.shard_tls_cert_name(MDB_RESOURCE_NAME, shardName, prefix)
        e.g., certs-mdb-sh-search-0-mdb-sh-0-cert
    """
    logger.info(f"Creating per-shard Search TLS certificates with prefix '{prefix}'...")

    for shard_idx in range(shard_count):
        shard_name = f"{mdb_resource_name}-{shard_idx}"
        secret_name = search_resource_names.shard_tls_cert_name(mdbs_resource_name, shard_name, prefix)

        additional_domains = [
            f"{search_resource_names.shard_service_name(mdbs_resource_name, shard_name)}.{namespace}.svc.cluster.local",
            f"{search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name)}.{namespace}.svc.cluster.local",
        ]

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=search_resource_names.shard_statefulset_name(mdbs_resource_name, shard_name),
            secret_name=secret_name,
            additional_domains=additional_domains,
        )
        logger.info(f"✓ Per-shard Search TLS certificate created: {secret_name}")

    logger.info(f"✓ All {shard_count} per-shard Search TLS certificates created")


def get_search_tester(mdb: MongoDB, username: str, password: str, use_ssl: bool = False) -> SearchTester:
    """Replaces both get_admin_search_tester and get_user_search_tester.
    Callers just pass the appropriate credentials."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_sharded(mdb, username, password, use_ssl=use_ssl, ca_path=ca_path)


def create_sharded_ca(issuer_ca_filepath: str, namespace: str, ca_configmap_name: str) -> str:
    ca = open(issuer_ca_filepath).read()
    configmap_data = {"ca-pem": ca, "mms-ca.crt": ca}
    create_or_update_configmap(namespace, ca_configmap_name, configmap_data)
    secret_data = {"ca.crt": ca}
    create_or_update_secret(namespace, ca_configmap_name, secret_data)
    return ca_configmap_name
