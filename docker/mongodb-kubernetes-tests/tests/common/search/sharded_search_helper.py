import json
import threading
from typing import Callable

import pymongo.errors
import yaml
from kubernetes import client
from kubetester import create_or_update_configmap, list_matching_pods
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


def per_cluster_search_tester(
    host: str,
    username: str,
    password: str,
) -> SearchTester:
    """SearchTester pinned to a single cluster's mongod/mongos `host` (`name:port`).

    directConnection=true so RS topology discovery does not silently follow back to
    the primary — the connected pod serves the query. `$search` is a read aggregation
    any node can serve, so readPreference=secondaryPreferred lets a secondary answer.
    TLS + the issuer CA are always on (the source runs requireTLS).
    """
    conn_str = (
        f"mongodb://{username}:{password}@{host}/"
        "?directConnection=true&readPreference=secondaryPreferred&authSource=admin"
    )
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


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
    LoadBalancerClientCert(): {prefix}-{name}-search-lb-{clusterIndex}-cert and
    {prefix}-{name}-search-lb-{clusterIndex}-client-cert. One server + client cert is
    created per cluster index, named to match that cluster's Envoy Deployment. Each cert
    carries SANs for *all* clusters' proxy Services so it stays valid wherever it lands.
    Pass `cluster_indexes=[0, 1, ...]` in multi-cluster setups.

    The server cert SAN list covers both per-shard proxy Services and the cluster-level
    proxy Service that mongos uses (the cluster-level Service exists for all sharded
    deployments, including single-cluster).
    """
    if cluster_indexes is None:
        cluster_indexes = [cluster_index]

    logger.info(f"Creating managed LB certificates for cluster_indexes={cluster_indexes}...")

    additional_domains = []
    for ci in cluster_indexes:
        for i in range(shard_count):
            shard_name = f"{mdb_resource_name}-{i}"
            proxy_svc = search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name, ci)
            additional_domains.append(f"{proxy_svc}.{namespace}.svc.cluster.local")
        additional_domains.append(search_resource_names.mc_proxy_svc_fqdn(mdbs_resource_name, namespace, ci))

    for ci in cluster_indexes:
        deployment_name = search_resource_names.lb_deployment_name(mdbs_resource_name, ci)
        lb_server_cert_name = search_resource_names.lb_server_cert_name(mdbs_resource_name, tls_cert_prefix, ci)
        lb_client_cert_name = search_resource_names.lb_client_cert_name(mdbs_resource_name, tls_cert_prefix, ci)

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=deployment_name,
            replicas=1,
            service_name=deployment_name,
            additional_domains=additional_domains,
            secret_name=lb_server_cert_name,
            api_client=api_client,
        )
        logger.info(f"LB server certificate created: {lb_server_cert_name}")

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=f"{deployment_name}-client",
            replicas=1,
            service_name=deployment_name,
            additional_domains=[f"*.{namespace}.svc.cluster.local"],
            secret_name=lb_client_cert_name,
            api_client=api_client,
        )
        logger.info(f"LB client certificate created: {lb_client_cert_name}")


def create_issuer_ca(issuer_ca_filepath: str, namespace: str, ca_configmap_name: str, api_client=None) -> str:
    ca = open(issuer_ca_filepath).read()
    configmap_data = {"ca-pem": ca, "mms-ca.crt": ca, "ca.crt": ca}
    create_or_update_configmap(namespace, ca_configmap_name, configmap_data, api_client)
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


def set_mongo_log_verbosity(mongo_client, level: int) -> None:
    """Set a mongod/mongos process COMMAND+NETWORK (and capped QUERY) log verbosity.

    ``level=2`` makes the process emit every command-completion record (regardless of
    slowOpThresholdMs) plus the gRPC egress-session open/close records — the data the
    cross-layer log analyzer needs to stitch ``$search`` queries across mongos → shard
    mongod → envoy → mongot. ``level=0`` restores defaults. Best-effort: logs and
    swallows errors so it never breaks a test or its teardown.
    """
    try:
        mongo_client.admin.command(
            "setParameter",
            1,
            logComponentVerbosity={
                "command": {"verbosity": level},
                "network": {"verbosity": level},
                "query": {"verbosity": min(level, 1)},
            },
        )
    except Exception as e:
        logger.info(f"set_mongo_log_verbosity({level}) failed: {type(e).__name__}: {e}")


class ShardStatePoller(threading.Thread):
    """Daemon thread that periodically logs, with wall-clock stamps, the per-shard
    ``$collStats`` doc distribution and the collection's ``$listSearchIndexes``
    per-host status. Use it to timestamp data movement and the
    ``DOES_NOT_EXIST → PENDING/INITIAL_SYNC → READY`` transitions on a mongot index
    during an onboarding / rebalance window. Start with ``.start()``, end with
    ``.stop()`` (it is a daemon, so it also dies with the process)::

        poller = ShardStatePoller(admin_tester, "sample_mflix", "movies", interval=2.0)
        poller.start()
        ...                      # drive the workload
        poller.stop()
    """

    def __init__(
        self,
        tester: SearchTester,
        database: str,
        collection: str,
        *,
        highlight_ids=None,
        interval: float = 2.0,
        label: str = "[poll]",
    ):
        super().__init__(daemon=True)
        self._tester = tester
        self._db = database
        self._coll = collection
        self._highlight = set(highlight_ids or ())
        self._interval = interval
        self._label = label
        self._stop = threading.Event()

    def _log_index_status(self) -> None:
        try:
            docs = list(
                self._tester.client[self._db][self._coll].aggregate([{"$listSearchIndexes": {}}], maxTimeMS=10_000)
            )
        except Exception as e:
            logger.info(f"{self._label} $listSearchIndexes error: {type(e).__name__}: {e}")
            return
        for d in docs:
            detail = ", ".join(
                f"{(sd.get('hostname') or '?')}{'(new)' if sd.get('hostname') in self._highlight else ''}:"
                f"{sd.get('status')}(q={sd.get('queryable')})"
                for sd in (d.get("statusDetail") or [])
            )
            logger.info(
                f"{self._label} index '{d.get('name')}' top={d.get('status')} q={d.get('queryable')} | {detail}"
            )

    def run(self) -> None:
        while not self._stop.is_set():
            try:
                log_shard_distribution(self._tester, self._db, self._coll, label=self._label)
                self._log_index_status()
            except Exception as e:
                logger.info(f"{self._label} poll error: {type(e).__name__}: {e}")
            self._stop.wait(self._interval)

    def stop(self) -> None:
        self._stop.set()


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
    """Let the balancer populate a freshly added shard with the collection's data.

    The balancer (6.0+) balances on data size and auto-splits ranges during
    migration, but a collection counts as balanced while the per-shard data spread
    is below 3 x chunkSize — with the default 128MB chunkSize that threshold is
    384MB, far above this suite's dataset, so a new shard would stay at 0 docs
    forever. Dropping the collection's chunkSize to 1MB (the minimum) lowers the
    threshold to 3MB and the balancer migrates ~1MB ranges onto the new shard on
    its own. Unlike ``reshardCollection`` (forceRedistribution), balancer
    migrations keep the collection and its mongot search index intact — the new
    shard's mongot resyncs the migrated ranges — so this composes with the
    search-availability assertions.

    Idempotent: re-applying the chunkSize is a no-op and an already-populated
    shard satisfies the poll immediately. Returns the final per-shard counts;
    raises on timeout so a real gap is loud.
    """
    ns = f"{database_name}.{collection_name}"
    client = admin_tester.client

    log_shard_distribution(admin_tester, database_name, collection_name, label="[before rebalance]")
    client.admin.command("configureCollectionBalancing", ns, chunkSize=1)
    ensure_balancer_running(admin_tester)

    # Balancer migrations are async; $collStats reflects each migrated range once
    # it lands (donor orphan cleanup lags, so totals may transiently overcount —
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
        msg=f"balancer to move >= {min_docs} docs of {ns} onto new shard {new_shard_name}",
    )
    final = log_shard_distribution(admin_tester, database_name, collection_name, label="[rebalanced]")
    logger.info(f"new shard {new_shard_name} now holds {final.get(new_shard_name, 0)} docs of {ns}")
    return final


def routing_ready_groups(namespace: str, mdbs_resource_name: str) -> list[str]:
    """The one-way routing-readiness latch from the ``<name>-search-state`` ConfigMap.
    A shard's mongot group is pending iff it is not listed here."""
    data = KubernetesTester.read_configmap(
        namespace, search_resource_names.search_state_configmap_name(mdbs_resource_name)
    )
    return json.loads(data["state"]).get("routingReadyMongotGroups") or []


def shard_route_from_lds(namespace: str, mdbs_resource_name: str, shard_name: str) -> tuple[str, dict[str, str]]:
    """Return ``(target_cluster, request_headers)`` for the shard's Envoy filter
    chain from the LB config CM's lds.json. A pending shard routes to
    ``mongot_cluster_level_cluster`` with the routed_from_another_shard header; a
    latched shard routes to its own ``mongot_<shard>_cluster`` with no header."""
    sni = search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name)
    lds = json.loads(
        KubernetesTester.read_configmap(namespace, search_resource_names.lb_configmap_name(mdbs_resource_name))[
            "lds.json"
        ]
    )
    for listener in lds.get("resources", []):
        for chain in listener.get("filter_chains", []):
            server_names = chain.get("filter_chain_match", {}).get("server_names", [])
            if not any(s.startswith(f"{sni}.") for s in server_names):
                continue
            hcm = next(
                f["typed_config"]
                for f in chain["filters"]
                if f["name"] == "envoy.filters.network.http_connection_manager"
            )
            route = hcm["route_config"]["virtual_hosts"][0]["routes"][0]
            headers = {h["header"]["key"]: h["header"]["value"] for h in route.get("request_headers_to_add", [])}
            return route["route"]["cluster"], headers
    raise AssertionError(f"no filter chain for shard {shard_name} (SNI {sni}) in lds.json")


def wait_for_envoy_loaded_shard_chain(
    namespace: str, mdbs_resource_name: str, shard_name: str, timeout: int = 300
) -> None:
    """Block until every Envoy pod's MOUNTED lds.json contains the shard's filter chain.

    The controller writing the LB ConfigMap is not enough: kubelet propagates the
    mounted file with up to ~1min lag, and only the mounted file is what Envoy
    hot-reloads (the filesystem-xDS watch fires within ms of the file swap).
    Reading the file inside the envoy container is therefore the earliest
    trustworthy "this proxy can serve the shard's SNI" signal — gate data
    movement onto a freshly added shard on it."""
    sni = search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name)
    dep_name = search_resource_names.lb_deployment_name(mdbs_resource_name)

    def chain_mounted():
        pods = list_matching_pods(namespace, name_prefix=f"{dep_name}-")
        if not pods:
            return False, f"no envoy pods with prefix {dep_name}-"
        for pod in pods:
            try:
                content = KubernetesTester.run_command_in_pod_container(
                    pod.metadata.name, namespace, ["cat", "/etc/envoy/lds.json"], container="envoy"
                )
            except Exception as e:
                return False, f"{pod.metadata.name}: exec failed: {e}"
            if sni not in content:
                return False, f"{pod.metadata.name}: mounted lds.json has no chain for SNI {sni}"
        return True, f"all {len(pods)} envoy pods mounted the {shard_name} chain"

    run_periodically(
        chain_mounted, timeout=timeout, sleep_time=5, msg=f"envoy pods to mount the {shard_name} filter chain"
    )


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

    Preserves any existing fields under spec.clusters[0].loadBalancer.managed.
    """
    mdbs.load()
    mdbs["spec"]["clusters"][0]["loadBalancer"].setdefault("managed", {})["deployment"] = deployment_config
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
