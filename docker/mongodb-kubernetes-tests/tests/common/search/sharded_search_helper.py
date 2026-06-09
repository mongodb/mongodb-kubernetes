from typing import Callable

import pymongo.errors
import yaml
from kubernetes import client
from kubetester import create_or_update_configmap, create_or_update_secret
from kubetester.certs import create_tls_certs
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.movies_search_helper import EmbeddedMoviesSearchHelper, SampleMoviesSearchHelper
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
    cluster_index: int = 0,  # default 0 preserves single-cluster behaviour
    api_client=None,
):
    """
    Create per-shard TLS certificates for MongoDBSearch resource.

    For each shard, creates a certificate with DNS names for:
    - The mongot service: {search-name}-search-{cluster_index}-{shardName}-svc.{namespace}.svc.cluster.local
    - The proxy service: {search-name}-search-{cluster_index}-{shardName}-proxy-svc.{namespace}.svc.cluster.local

    Secret naming: search_resource_names.shard_tls_cert_name(mdbs_resource_name, shardName, prefix, cluster_index)
    e.g., certs-mdb-sh-search-0-mdb-sh-0-cert
    """
    logger.info(f"Creating per-shard Search TLS certificates with prefix '{prefix}' for cluster {cluster_index}...")

    for shard_idx in range(shard_count):
        shard_name = f"{mdb_resource_name}-{shard_idx}"
        secret_name = search_resource_names.shard_tls_cert_name(mdbs_resource_name, shard_name, prefix, cluster_index)

        additional_domains = [
            f"{search_resource_names.shard_service_name(mdbs_resource_name, shard_name, cluster_index)}.{namespace}.svc.cluster.local",
            f"{search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name, cluster_index)}.{namespace}.svc.cluster.local",
        ]

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=search_resource_names.shard_statefulset_name(mdbs_resource_name, shard_name, cluster_index),
            secret_name=secret_name,
            additional_domains=additional_domains,
            api_client=api_client,
        )
        logger.info(f"Per-shard Search TLS certificate created: {secret_name}")

    logger.info(f"All {shard_count} per-shard Search TLS certificates created for cluster {cluster_index}")


def get_search_tester(mdb: MongoDB, username: str, password: str, use_ssl: bool = False) -> SearchTester:
    """Replaces both get_admin_search_tester and get_user_search_tester.
    Callers just pass the appropriate credentials."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_sharded(mdb, username, password, use_ssl=use_ssl, ca_path=ca_path)


def sharded_search_tester(
    mdb_resource_name: str,
    namespace: str,
    username: str,
    password: str,
    use_ssl: bool = True,
) -> SearchTester:
    """Name-addressed SearchTester through the sharded source's mongos-0 (no ``mdb`` object).

    Byte-identical conn-str to ``SearchTester.for_sharded`` so name-based Layer-3/4 testers
    match the object-based path.
    """
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    conn_str = (
        f"mongodb://{username}:{password}@"
        f"{mdb_resource_name}-mongos-0.{mdb_resource_name}-svc.{namespace}.svc.cluster.local:27017/"
        f"?authSource=admin"
    )
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


def mc_sharded_search_tester(
    mdb_resource_name: str,
    namespace: str,
    cluster_index: int,
    username: str,
    password: str,
    use_ssl: bool = True,
) -> SearchTester:
    """Name-addressed SearchTester through one cluster's mongos (MC sharded source).

    The per-pod mongos headless Service ``{mdb}-mongos-{clusterIdx}-0-svc`` is reachable
    cross-cluster via Istio; ``directConnection=true`` pins the driver to that cluster's
    mongos so a per-cluster data-path assertion stays on that cluster. Byte-identical
    conn-str to ``q3``'s per-cluster mongos tester.
    """
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    mongos_host = f"{mdb_resource_name}-mongos-{cluster_index}-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{mongos_host}/?directConnection=true&authSource=admin"
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


def get_shard_mongod_tester(
    mdb: MongoDB,
    shard_index: int,
    member_index: int,
    username: str,
    password: str,
    *,
    use_ssl: bool = True,
) -> SearchTester:
    """SearchTester pinned to one shard's mongod member via directConnection.

    Per-shard mongod FQDN follows the operator's shard-service convention:
    ``<mdb>-<shardIdx>-<podIdx>.<mdb>-sh.<ns>.svc.cluster.local:27017``.
    """
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    host = f"{mdb.name}-{shard_index}-{member_index}.{mdb.name}-sh.{mdb.namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{host}/?directConnection=true&authSource=admin"
    return SearchTester(conn_str, use_ssl=use_ssl, ca_path=ca_path)


def create_lb_certificates(
    namespace: str,
    issuer: str,
    shard_count: int,
    mdb_resource_name: str,
    mdbs_resource_name: str,
    tls_cert_prefix: str,
    cluster_index: int = 0,  # default 0 preserves single-cluster behaviour
    cluster_indexes: list[int] | None = None,
    api_client=None,
):
    """Create TLS certificates for the operator-managed load balancer (Envoy proxy).

    Secret names must match what the operator expects per LoadBalancerServerCert() and
    LoadBalancerClientCert(): {prefix}-{name}-search-lb-0-cert and
    {prefix}-{name}-search-lb-0-client-cert. The secret name does not vary per cluster
    (operator mounts the same cert in every member cluster's Envoy), so the cert must
    carry SANs for *all* clusters' proxy Services. Pass `cluster_indexes=[0, 1, ...]`
    in multi-cluster setups so the SAN list covers every cluster's proxy hostnames.

    The server cert SAN list covers both per-shard proxy Services and the cluster-level
    proxy Service that mongos uses (M3: cluster-level Service is created for all sharded,
    including single-cluster).
    """
    if cluster_indexes is None:
        cluster_indexes = [cluster_index]

    logger.info(f"Creating managed LB certificates for cluster_indexes={cluster_indexes}...")

    lb_server_cert_name = search_resource_names.lb_server_cert_name(mdbs_resource_name, tls_cert_prefix)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(mdbs_resource_name, tls_cert_prefix)

    # Build SANs: per-shard proxy Services + cluster-level proxy Service for mongos,
    # across every cluster index so the single Envoy cert is valid in any member cluster.
    additional_domains = []
    for ci in cluster_indexes:
        for i in range(shard_count):
            shard_name = f"{mdb_resource_name}-{i}"
            proxy_svc = search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name, ci)
            additional_domains.append(f"{proxy_svc}.{namespace}.svc.cluster.local")
        additional_domains.append(search_resource_names.mc_proxy_svc_fqdn(mdbs_resource_name, namespace, ci))

    # Create server certificate
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(mdbs_resource_name),
        replicas=1,
        service_name=search_resource_names.lb_deployment_name(mdbs_resource_name),
        additional_domains=additional_domains,
        secret_name=lb_server_cert_name,
        api_client=api_client,
    )
    logger.info(f"LB server certificate created: {lb_server_cert_name}")

    # Create client certificate
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(mdbs_resource_name)}-client",
        replicas=1,
        service_name=search_resource_names.lb_deployment_name(mdbs_resource_name),
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name=lb_client_cert_name,
        api_client=api_client,
    )
    logger.info(f"LB client certificate created: {lb_client_cert_name}")


def create_issuer_ca(issuer_ca_filepath: str, namespace: str, ca_configmap_name: str, api_client=None) -> str:
    ca = open(issuer_ca_filepath).read()
    configmap_data = {"ca-pem": ca, "mms-ca.crt": ca}
    create_or_update_configmap(namespace, ca_configmap_name, configmap_data, api_client)
    secret_data = {"ca.crt": ca}
    create_or_update_secret(namespace, ca_configmap_name, secret_data, api_client=api_client)
    return ca_configmap_name


def verify_mongos_search_config(namespace: str, mdb_resource_name: str):
    """Verify mongos has mongotHost and searchIndexManagementHostAndPort configured."""
    mongos_pod = f"{mdb_resource_name}-mongos-0"

    def check_mongos_config():
        try:
            config = KubernetesTester.run_command_in_pod_container(
                mongos_pod, namespace, ["cat", f"/var/lib/mongodb-mms-automation/workspace/mongos-{mongos_pod}.conf"]
            )

            has_mongot_host = "mongotHost" in config
            has_search_mgmt = "searchIndexManagementHostAndPort" in config

            status = f"mongotHost={has_mongot_host}, searchMgmt={has_search_mgmt}"
            return has_mongot_host and has_search_mgmt, status
        except Exception as e:
            return False, f"Error: {e}"

    run_periodically(check_mongos_config, timeout=300, sleep_time=10, msg="mongos search config")
    logger.info("Mongos has correct search configuration")


def verify_sharded_mongod_parameters(
    namespace: str,
    mdb_resource_name: str,
    mdbs_name: str,
    shard_count: int,
    expected_host_fn: Callable[[str], str],
):
    """Verify each shard's mongod has correct mongotHost and searchIndexManagementHostAndPort.

    expected_host_fn(shard_name) -> expected host:port string.
    """

    def check_mongod_parameters():
        all_correct = True
        status_msgs = []

        for shard_idx in range(shard_count):
            shard_name = f"{mdb_resource_name}-{shard_idx}"
            pod_name = f"{shard_name}-0"

            try:
                mongod_config = yaml.safe_load(
                    KubernetesTester.run_command_in_pod_container(
                        pod_name, namespace, ["cat", "/data/automation-mongod.conf"]
                    )
                )
                set_parameter = mongod_config.get("setParameter", {})
                mongot_host = set_parameter.get("mongotHost", "")
                search_index_host = set_parameter.get("searchIndexManagementHostAndPort", "")

                expected_mongot_host_port = expected_host_fn(shard_name)

                if mongot_host != expected_mongot_host_port:
                    all_correct = False
                    status_msgs.append(
                        f"Shard {shard_name}: mongotHost={mongot_host}, expected={expected_mongot_host_port}"
                    )
                elif search_index_host != expected_mongot_host_port:
                    all_correct = False
                    status_msgs.append(
                        f"Shard {shard_name}: searchIndexMgmt={search_index_host}, expected={expected_mongot_host_port}"
                    )
                else:
                    status_msgs.append(f"Shard {shard_name}: hosts correctly set to {expected_mongot_host_port}")

            except Exception as e:
                all_correct = False
                status_msgs.append(f"Shard {shard_name}: Error - {e}")

        return all_correct, "\n".join(status_msgs)

    run_periodically(check_mongod_parameters, timeout=300, sleep_time=10, msg="mongod search parameters")
    logger.info("All shards have correct mongod search parameters")


def log_shard_distribution(
    admin_tester: SearchTester,
    database_name: str,
    collection_name: str,
    label: str = "",
) -> dict[str, int]:
    """Log and return the per-shard document distribution for a collection.

    Uses ``$collStats`` (one doc per shard); the ``shard`` field is the shard
    name as registered in the sharded cluster (e.g. ``mdb-sh-routed-2``).
    """
    coll = admin_tester.client[database_name][collection_name]
    stats = list(coll.aggregate([{"$collStats": {"storageStats": {}}}]))
    per_shard = {s.get("shard") or s.get("host"): s.get("storageStats", {}).get("count", 0) for s in stats}
    total = sum(per_shard.values())
    prefix = f"{label} " if label else ""
    logger.info(f"{prefix}per-shard '{database_name}.{collection_name}' counts: {per_shard} total={total}")
    return per_shard


def get_balancer_status(admin_tester: SearchTester) -> dict:
    """Return (and log) the sharded-cluster balancer status from mongos."""
    status = admin_tester.client.admin.command("balancerStatus")
    logger.info(
        f"balancer status: mode={status.get('mode')} inBalancerRound={status.get('inBalancerRound')} "
        f"numBalancerRounds={status.get('numBalancerRounds')}"
    )
    return status


def ensure_balancer_running(admin_tester: SearchTester) -> None:
    """Start the balancer if it is not already in 'full' mode.

    A freshly added shard is populated by the balancer migrating chunks onto it.
    Unlike ``reshardCollection``/``shardAndDistributeCollection``, balancer chunk
    migrations preserve the collection and its search indexes (mongot resyncs the
    migrated ranges on the new shard's mongot), so search availability is kept.
    """
    status = get_balancer_status(admin_tester)
    if status.get("mode") != "full":
        logger.info("balancer is not in 'full' mode — starting it via balancerStart")
        admin_tester.client.admin.command("balancerStart")
        get_balancer_status(admin_tester)
    else:
        logger.info("balancer already running (mode=full)")


def _collection_chunks(admin_tester: SearchTester, ns: str) -> tuple[object, list[dict]]:
    client = admin_tester.client
    coll = client.config.collections.find_one({"_id": ns})
    if coll is None:
        raise AssertionError(f"{ns} is not sharded (no config.collections entry)")
    uuid = coll["uuid"]
    return uuid, list(client.config.chunks.find({"uuid": uuid}))


def redistribute_chunks_to_new_shard(
    admin_tester: SearchTester,
    database_name: str,
    collection_name: str,
    new_shard_name: str,
    *,
    min_docs: int = 50,
    timeout: int = 300,
    sleep_time: int = 10,
) -> dict[str, int]:
    """Give a freshly added shard a fair share of a hashed-sharded collection's data.

    Why the balancer alone does NOT do this: the balancer migrates only *whole*
    chunks and only once the per-shard *data-size* imbalance crosses a threshold.
    A freshly sharded sample collection here has just one chunk per pre-existing
    shard, so there is nothing for the balancer to hand the new (empty) shard — it
    stays at 0 docs no matter how many balancer rounds run (observed: balancer in
    mode=full, hundreds of rounds, distribution unchanged). To populate the new
    shard we explicitly split the donor chunks at their median and ``moveChunk`` a
    proportional share onto it. Unlike ``reshardCollection`` (forceRedistribution),
    chunk migration keeps the collection and its mongot search index intact — the
    new shard's mongot resyncs the migrated range — so it composes with the
    search-availability assertion.

    Idempotent: if the new shard already owns chunks, the split/move is skipped.
    Returns the final per-shard counts; raises on timeout so a real gap is loud.
    """
    ns = f"{database_name}.{collection_name}"
    client = admin_tester.client

    ensure_balancer_running(admin_tester)
    log_shard_distribution(admin_tester, database_name, collection_name, label="[before redistribute]")

    _, chunks = _collection_chunks(admin_tester, ns)
    shard_ids = [s["_id"] for s in client.config.shards.find()]
    num_shards = len(shard_ids)
    already_owned = sum(1 for ch in chunks if ch["shard"] == new_shard_name)

    if already_owned == 0:
        logger.info(
            f"{new_shard_name} owns 0 chunks of {ns} (total chunks={len(chunks)}); "
            f"splitting donor chunks and moving a ~1/{num_shards} share onto it"
        )
        # Median-split every existing chunk so there are movable pieces.
        for ch in list(chunks):
            try:
                client.admin.command("split", ns, bounds=[ch["min"], ch["max"]])
            except pymongo.errors.OperationFailure as e:
                logger.info(f"split skipped for chunk on {ch['shard']} ({ch['min']}..{ch['max']}): {e}")
        _, chunks = _collection_chunks(admin_tester, ns)
        donors = [ch for ch in chunks if ch["shard"] != new_shard_name]
        move_count = max(1, len(chunks) // num_shards)
        for ch in donors[:move_count]:
            logger.info(f"moveChunk {ch['min']}..{ch['max']} from {ch['shard']} -> {new_shard_name}")
            client.admin.command("moveChunk", ns, bounds=[ch["min"], ch["max"]], to=new_shard_name)
    else:
        logger.info(f"{new_shard_name} already owns {already_owned} chunk(s) of {ns}; skipping split/move")

    # moveChunk returns once the range is on the new shard's mongod; $collStats then
    # reflects it (donor orphan cleanup lags, so totals may transiently overcount —
    # we only gate on the new shard's own count).
    def populated():
        per_shard = log_shard_distribution(
            admin_tester, database_name, collection_name, label=f"[await {new_shard_name}]"
        )
        count = per_shard.get(new_shard_name, 0)
        return count >= min_docs, f"{new_shard_name}={count} docs (need >= {min_docs})"

    run_periodically(
        populated,
        timeout=timeout,
        sleep_time=sleep_time,
        msg=f"new shard {new_shard_name} to hold >= {min_docs} docs of {ns}",
    )
    final = log_shard_distribution(admin_tester, database_name, collection_name, label="[redistributed]")
    logger.info(f"new shard {new_shard_name} now holds {final.get(new_shard_name, 0)} docs of {ns}")
    return final


def verify_text_search_query(search_tester: SearchTester):
    """Execute a text search for 'star wars' and verify results are returned."""
    movies_helper = SampleMoviesSearchHelper(search_tester)

    def execute_search():
        try:
            results = movies_helper.text_search_movies("star wars")

            result_count = len(results)
            logger.info(f"Search returned {result_count} results")
            for r in results:
                logger.debug(f"  - {r.get('title')} (score: {r.get('score')})")

            if result_count > 0:
                return True, f"Search returned {result_count} results"
            return False, "Search returned no results"
        except pymongo.errors.PyMongoError as e:
            return False, f"Error: {e}"

    run_periodically(execute_search, timeout=60, sleep_time=5, msg="search query to succeed")
    logger.info("Text search query executed successfully through mongos")


def verify_search_results_from_all_shards(search_tester: SearchTester):
    """Verify wildcard search returns all documents (minus 1 untokenized '$' doc)."""
    movies_helper = SampleMoviesSearchHelper(search_tester)
    total_docs = search_tester.client["sample_mflix"]["movies"].count_documents({})
    logger.info(f"Total documents in collection: {total_docs}")

    # One document with title "$" is not tokenized by Lucene, won't appear in wildcard results
    expected_docs = total_docs - 1

    def execute_all_docs_search():
        try:
            results = movies_helper.wildcard_search_movies()
        except pymongo.errors.OperationFailure as e:
            logger.info(f"Search not ready yet: {e}")
            return False, f"Search failed: {e}"
        search_count = len(results)
        logger.info(f"Search through mongos returned {search_count} documents")

        if search_count == expected_docs:
            return True, ""
        return (
            False,
            f"Search returned {search_count} documents, expected {expected_docs}",
        )

    run_periodically(execute_all_docs_search, timeout=120, sleep_time=5, msg="search query for all docs")
    logger.info("Search results for all documents verified.")


def verify_vector_search_before_and_after_sharding(
    search_tester: SearchTester,
    admin_search_tester: SearchTester,
):
    """Verify vector search returns consistent results before and after sharding embedded_movies."""
    emb_helper = EmbeddedMoviesSearchHelper(search_tester)

    # Generate query vector by calling the Voyage embedding API
    query_vector = emb_helper.generate_query_vector("war movies")

    # Count total documents with embeddings to use as the limit
    total_docs = emb_helper.count_documents_with_embeddings()
    logger.info(f"Total documents with embeddings: {total_docs}")

    # Run vector search before sharding
    results_before = emb_helper.vector_search(query_vector, limit=total_docs)
    count_before = len(results_before)
    logger.info(f"Vector search before sharding: {count_before} results")
    assert count_before > 0, "Vector search returned no results before sharding"

    # Shard the embedded_movies collection (requires admin)
    admin_search_tester.shard_and_distribute_collection("sample_mflix", "embedded_movies")
    logger.info("embedded_movies collection sharded")

    # Resharding drops search indexes — recreate and wait for ready
    emb_helper.create_vector_search_index()
    emb_helper.wait_for_vector_search_index(timeout=300)
    logger.info("Vector search index recreated after resharding")

    # Run vector search after sharding with the same query vector and verify same count.
    # Catch OperationFailure because mongot shards may still be in INITIAL_SYNC after resharding.
    def verify_vector_search_after_sharding():
        try:
            results_after = emb_helper.vector_search(query_vector, limit=total_docs)
        except pymongo.errors.OperationFailure as e:
            logger.info(f"Vector search not ready yet: {e}")
            return False, f"Vector search failed: {e}"
        count_after = len(results_after)
        logger.info(f"Vector search after sharding: {count_after} results")
        if count_after == count_before:
            return True, f"Vector search returned {count_after} results (matches pre-sharding count)"
        return False, f"Vector search returned {count_after} results, expected {count_before}"

    run_periodically(
        verify_vector_search_after_sharding, timeout=300, sleep_time=10, msg="vector search after sharding"
    )
    logger.info(f"Vector search returns consistent {count_before} results after sharding")


def patch_envoy_deployment_configuration(
    mdbs: MongoDBSearch,
    deployment_config: dict,
    timeout: int = 300,
):
    """Patch the MongoDBSearch CR with a deployment override and wait for Running.

    Preserves any existing fields under spec.loadBalancer.managed.
    """
    mdbs.load()
    mdbs["spec"]["loadBalancer"].setdefault("managed", {})["deployment"] = deployment_config
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=timeout)


def verify_envoy_deployment_override(
    namespace: str,
    mdbs_resource_name: str,
    expected_container_names: list[str],
    expected_labels: dict[str, str] | None = None,
    expected_annotations: dict[str, str] | None = None,
):
    """Verify that deploymentConfiguration overrides were applied to the Envoy Deployment."""
    envoy_deployment_name = search_resource_names.lb_deployment_name(mdbs_resource_name)
    apps_v1 = client.AppsV1Api()
    deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)

    containers = deployment.spec.template.spec.containers
    actual_names = [c.name for c in containers]
    for name in expected_container_names:
        assert name in actual_names, f"container {name!r} missing, found: {actual_names}"
    assert len(containers) == len(
        expected_container_names
    ), f"Expected {len(expected_container_names)} containers, got {len(containers)}: {actual_names}"

    if expected_labels:
        for k, v in expected_labels.items():
            actual = deployment.metadata.labels.get(k)
            assert actual == v, f"label {k!r}: expected {v!r}, got {actual!r}"

    if expected_annotations:
        for k, v in expected_annotations.items():
            actual = (deployment.metadata.annotations or {}).get(k)
            assert actual == v, f"annotation {k!r}: expected {v!r}, got {actual!r}"

    logger.info(f"Envoy Deployment {envoy_deployment_name} overrides verified")
