"""Reusable pytest test-class mixins for the managed-LB search bootstrap.

**Consumer pattern — DECLARE BASES IN REVERSE EXECUTION ORDER:**

    pytestmark = pytest.mark.e2e_<file_specific_marker>

    class TestBootstrap(
        SearchDeploymentTests,        # Layer 2 — runs second
        MongoDBRsDeploymentTests,     # Layer 1 — runs FIRST
    ):
        pass

    class TestSampleData(
        SearchSampleDataAndIndexTests,   # runs restore+indexes tests
        SearchE2EFixtures,               # supplies hooks consumed by Layer 3
    ):
        pass

    class TestUnique(_ConnToolRsConfig, SearchE2EFixtures):
        def test_unique_scenario(self, mdb, mdbs, namespace):
            ...

Why bases are listed in reverse: pytest's class collector emits inherited
test methods in reversed order. So the FIRST base in the declaration is
emitted LAST. Declaring bases in reverse-execution order makes the
bootstrap fire in the proper order: MongoDB, Search, Restore+Data, actual test on top
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

from kubernetes import client
from pytest import fixture

from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.rs_search_helper import (
    create_rs_lb_certificates,
    create_rs_search_tls_cert,
    get_rs_search_tester,
    verify_rs_mongod_parameters,
)
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
    get_search_tester,
    verify_sharded_mongod_parameters,
)
from tests.conftest import get_default_operator, is_multi_cluster, get_multi_cluster_operator, get_central_cluster_name, get_multi_cluster_operator_installation_config, \
    get_central_cluster_client, get_member_cluster_clients, get_member_cluster_names
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)


# ---------------------------------------------------------------------------
# Per-layer configuration dataclasses. Subclasses override the matching
# ``build_*_config`` method below to mutate fields via ``super()``.
# ---------------------------------------------------------------------------


@dataclass
class MongoDBRsDeploymentConfig:
    mdb_resource_name: str = "mdb-rs"
    rs_members: int = 3
    set_tls: bool = True

    admin_user_name: str = ""
    admin_user_password: str = ""
    user_name: str = ""
    user_password: str = ""
    mongot_user_name: str = "search-sync-source"
    mongot_user_password: str = "search-sync-source-user-password"

    def __post_init__(self) -> None:
        if not self.admin_user_name:
            self.admin_user_name = f"{self.mdb_resource_name}-admin-user"
        if not self.admin_user_password:
            self.admin_user_password = f"{self.admin_user_name}-pass"
        if not self.user_name:
            self.user_name = f"{self.mdb_resource_name}-user"
        if not self.user_password:
            self.user_password = f"{self.user_name}-pass"

    @property
    def ca_configmap_name(self) -> str:
        return f"{self.mdb_resource_name}-ca"


@dataclass
class SearchDeploymentConfig:
    mdbs_resource_name: Optional[str] = None
    mdbs_tls_cert_prefix: str = "certs"
    mdbs_fixture_yaml: str = "search-rs-managed-lb.yaml"
    envoy_proxy_port: int = 27028
    mongot_replicas: int = 1


@dataclass
class SampleDataAndIndexConfig:
    search_index_name: str = "default"
    smoke_query_text: str = "Apollo"
    smoke_query_path: str = "title"
    sample_database: str = "sample_mflix"
    sample_collection: str = "movies"
    extra_doc_count: int = 10_000
    extra_doc_batch_size: int = 1000


class SearchE2EHooks:
    def build_sample_data_and_index_config(self) -> SampleDataAndIndexConfig:
        return SampleDataAndIndexConfig()

    def _admin_tester(self, mdb) -> SearchTester:
        raise NotImplementedError(
            "consumer test class must inherit a topology fixtures class "
            "(SearchE2EFixtures / SearchShardedE2EFixtures)"
        )

    def _user_tester(self, mdb) -> SearchTester:
        raise NotImplementedError(
            "consumer test class must inherit a topology fixtures class "
            "(SearchE2EFixtures / SearchShardedE2EFixtures)"
        )

    def _post_restore_setup(self, mdb) -> None:
        """Override point for steps that run between restore (+synthetic
        corpus) and index creation. Default no-op;
        ``SearchShardedSampleDataAndIndex`` overrides to shard and
        distribute the collection."""
        return None


# ---------------------------------------------------------------------------
# Topology fixtures + default ``build_*_config`` stubs. Each topology
# fixtures class extends ``SearchE2EHooks`` and provides its concrete
# tester factories.
# ---------------------------------------------------------------------------


class SearchE2EFixtures(SearchE2EHooks):
    # Default config-builder hooks. Consumers override via ``super()``.
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return MongoDBRsDeploymentConfig()

    def build_search_deployment_config(self) -> SearchDeploymentConfig:
        return SearchDeploymentConfig()

    def effective_mdbs_resource_name(self) -> str:
        """Resolve the MongoDBSearch CR name. Falls back to the linked
        MongoDB resource name when ``SearchDeploymentConfig.mdbs_resource_name``
        is unset — matches the convention used across the search e2es.
        """
        sd = self.build_search_deployment_config()
        if sd.mdbs_resource_name:
            return sd.mdbs_resource_name
        return self.build_mongodb_rs_config().mdb_resource_name

    @fixture(scope="class")
    def ca_configmap(self, issuer_ca_filepath: str, namespace: str) -> str:
        cfg = self.build_mongodb_rs_config()
        return create_issuer_ca(issuer_ca_filepath, namespace, cfg.ca_configmap_name)

    @fixture(scope="function")
    def helper(self, namespace: str) -> SearchDeploymentHelper:
        cfg = self.build_mongodb_rs_config()
        return SearchDeploymentHelper(
            namespace=namespace,
            mdb_resource_name=cfg.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            ca_configmap_name=cfg.ca_configmap_name,
        )

    @fixture(scope="function")
    def mdb(self, namespace: str, ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
        cfg = self.build_mongodb_rs_config()
        return helper.create_rs_mdb(set_tls=cfg.set_tls)

    @fixture(scope="function")
    def mdbs(self, namespace: str) -> MongoDBSearch:
        sd_cfg = self.build_search_deployment_config()
        mongo_cfg = self.build_mongodb_rs_config()
        resource = MongoDBSearch.from_yaml(
            yaml_fixture(sd_cfg.mdbs_fixture_yaml),
            namespace=namespace,
            name=self.effective_mdbs_resource_name(),
        )
        if try_load(resource):
            return resource
        resource["spec"]["source"]["mongodbResourceRef"]["name"] = mongo_cfg.mdb_resource_name
        return resource

    @fixture(scope="function")
    def admin_user(self, helper: SearchDeploymentHelper) -> MongoDBUser:
        cfg = self.build_mongodb_rs_config()
        return helper.admin_user_resource(cfg.admin_user_name)

    @fixture(scope="function")
    def user(self, helper: SearchDeploymentHelper) -> MongoDBUser:
        cfg = self.build_mongodb_rs_config()
        return helper.user_resource(cfg.user_name)

    @fixture(scope="function")
    def mongot_user(self, helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
        cfg = self.build_mongodb_rs_config()
        return helper.mongot_user_resource(mdbs, cfg.mongot_user_name)

    @fixture(scope="module")
    def search_tools_pod_api_client(self):
        """ApiClient that hosts the mongorestore tools pod.

        Default ``None`` → operator-cluster client (sufficient for any
        topology where the MongoDB service FQDNs resolve from the
        operator cluster).
        """
        return None

    @fixture(scope="module")
    def search_tools_pod(self, namespace: str, search_tools_pod_api_client) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace, api_client=search_tools_pod_api_client)

    def _admin_tester(self, mdb: MongoDB) -> SearchTester:
        cfg = self.build_mongodb_rs_config()
        return get_rs_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password, use_ssl=True)

    def _user_tester(self, mdb: MongoDB) -> SearchTester:
        cfg = self.build_mongodb_rs_config()
        return get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)


# ---------------------------------------------------------------------------
# Bootstrap test mixins. Each owns its config + bootstrap test methods.
# ---------------------------------------------------------------------------

class InstallOperatorTests:
    def test_install_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        if not is_multi_cluster():
            operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
        else:
            operator = get_multi_cluster_operator(namespace,
                                                  central_cluster_name=get_central_cluster_name(),
                                                  multi_cluster_operator_installation_config=get_multi_cluster_operator_installation_config(namespace),
                                                  central_cluster_client=get_central_cluster_client(),
                                                  member_cluster_clients=get_member_cluster_clients(),
                                                  member_cluster_names=get_member_cluster_names())
        operator.assert_is_running()


class MongoDBRsDeploymentTests(SearchE2EFixtures):
    """MongoDB replica-set deployment bootstrap.

    Operator install → optional OM → RS TLS certs → MongoDB resource
    (creates and waits Running) → users.
    """



    @skip_if_cloud_manager
    def test_create_ops_manager(self, namespace: str):
        ops_manager = get_ops_manager(namespace)
        assert ops_manager is not None
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_install_tls_certificates(self, helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
        cfg = self.build_mongodb_rs_config()
        helper.install_rs_tls_certificates(issuer, members=cfg.rs_members)

    def test_create_database_resource(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    def test_create_users(
        self,
        helper: SearchDeploymentHelper,
        admin_user: MongoDBUser,
        user: MongoDBUser,
        mongot_user: MongoDBUser,
    ):
        cfg = self.build_mongodb_rs_config()
        helper.deploy_users(
            admin_user,
            cfg.admin_user_password,
            user,
            cfg.user_password,
            mongot_user,
            cfg.mongot_user_password,
        )


class SearchDeploymentTests(SearchE2EFixtures):
    """Search deployment on top of an existing MongoDB.

    LB and search TLS certs → MongoDBSearch resource → envoy readiness
    → re-wait MongoDB to Running (mongod is rolling-restarted to pick
    up ``searchIndexManagementHostAndPort``) → verify the mongod
    parameter actually points at the envoy proxy.
    """

    def test_deploy_lb_certificates(self, namespace: str, issuer: str):
        sd_cfg = self.build_search_deployment_config()
        create_rs_lb_certificates(namespace, issuer, self.effective_mdbs_resource_name(), sd_cfg.mdbs_tls_cert_prefix)

    def test_create_search_tls_certificate(self, namespace: str, issuer: str):
        sd_cfg = self.build_search_deployment_config()
        create_rs_search_tls_cert(namespace, issuer, self.effective_mdbs_resource_name(), sd_cfg.mdbs_tls_cert_prefix)

    def test_create_search_resource(self, mdbs: MongoDBSearch):
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=600)

    def test_verify_envoy_deployment(self, namespace: str):
        envoy_deployment_name = search_resource_names.lb_deployment_name(self.effective_mdbs_resource_name())

        def check_envoy_deployment():
            try:
                apps_v1 = client.AppsV1Api()
                deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
                ready = deployment.status.ready_replicas or 0
                return ready >= 1, f"ready_replicas={ready}"
            except Exception as e:
                return False, f"Deployment {envoy_deployment_name} not found: {e}"

        run_periodically(
            check_envoy_deployment, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_deployment_name}"
        )

    def test_wait_for_database_ready(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_verify_mongod_parameters(self, namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
        sd_cfg = self.build_search_deployment_config()
        mongo_cfg = self.build_mongodb_rs_config()
        expected_host = search_resource_names.proxy_service_host(mdbs.name, namespace, sd_cfg.envoy_proxy_port)
        verify_rs_mongod_parameters(namespace, mongo_cfg.mdb_resource_name, mongo_cfg.rs_members, expected_host)


class SearchSampleDataAndIndexTests(SearchE2EHooks):
    def test_deploy_tools_pod(self, search_tools_pod: mongodb_tools_pod.ToolsPod):
        logger.info(f"Tools pod {search_tools_pod.pod_name} is ready")

    def test_restore_sample_database(self, mdb, search_tools_pod: mongodb_tools_pod.ToolsPod):
        sample_cfg = self.build_sample_data_and_index_config()
        self._admin_tester(mdb).mongorestore_from_url(
            archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
            ns_include=f"{sample_cfg.sample_database}.*",
            tools_pod=search_tools_pod,
        )

    def test_insert_synthetic_corpus(self, mdb):
        """Inflate the corpus with synthetic docs when configured.

        No-op when ``extra_doc_count == 0``. Idempotent — the helper
        short-circuits via ``count_documents({"synthetic": True}) >= count``.
        """
        sample_cfg = self.build_sample_data_and_index_config()
        if sample_cfg.extra_doc_count <= 0:
            logger.info("synthetic corpus inflation disabled (extra_doc_count=0)")
            return
        self._admin_tester(mdb).insert_synthetic_movies(
            sample_cfg.sample_database,
            sample_cfg.sample_collection,
            sample_cfg.extra_doc_count,
            batch_size=sample_cfg.extra_doc_batch_size,
        )

    def test_post_restore_setup(self, mdb):
        """Run the topology-specific post-restore step (sharded: shard+distribute)."""
        self._post_restore_setup(mdb)

    def test_create_search_index(self, mdb):
        sample_cfg = self.build_sample_data_and_index_config()
        tester = self._user_tester(mdb)
        tester.create_search_index(sample_cfg.sample_database, sample_cfg.sample_collection)
        tester.wait_for_search_indexes_ready(sample_cfg.sample_database, sample_cfg.sample_collection, timeout=300)

    def test_smoke_search_query_succeeds(self, mdb):
        sample_cfg = self.build_sample_data_and_index_config()
        tester = self._user_tester(mdb)
        pipeline = [
            {
                "$search": {
                    "index": sample_cfg.search_index_name,
                    "text": {"query": sample_cfg.smoke_query_text, "path": sample_cfg.smoke_query_path},
                }
            },
            {"$limit": 5},
        ]
        results = list(tester.client[sample_cfg.sample_database][sample_cfg.sample_collection].aggregate(pipeline))
        assert results, (
            f"smoke $search against {sample_cfg.sample_database}.{sample_cfg.sample_collection} "
            f"returned 0 docs — deployment is not wired"
        )


# ---------------------------------------------------------------------------
# Sharded variant — sibling to the RS mixins above.
# ---------------------------------------------------------------------------


@dataclass
class MongoDBShardedDeploymentConfig:
    """Sharded MongoDB + operator + users."""

    mdb_resource_name: str = "mdb-sh"
    shard_count: int = 2
    mongods_per_shard: int = 1
    mongos_count: int = 1
    config_server_count: int = 1
    set_tls_ca: bool = True

    # Derived from ``mdb_resource_name`` when unset — see RS config above.
    admin_user_name: str = ""
    admin_user_password: str = ""
    user_name: str = ""
    user_password: str = ""
    mongot_user_name: str = "search-sync-source"
    mongot_user_password: str = "search-sync-source-user-password"

    def __post_init__(self) -> None:
        if not self.admin_user_name:
            self.admin_user_name = f"{self.mdb_resource_name}-admin-user"
        if not self.admin_user_password:
            self.admin_user_password = f"{self.admin_user_name}-pass"
        if not self.user_name:
            self.user_name = f"{self.mdb_resource_name}-user"
        if not self.user_password:
            self.user_password = f"{self.user_name}-pass"

    @property
    def ca_configmap_name(self) -> str:
        return f"{self.mdb_resource_name}-ca"


class SearchShardedE2EFixtures(SearchE2EHooks):
    def build_mongodb_sharded_config(self) -> MongoDBShardedDeploymentConfig:
        return MongoDBShardedDeploymentConfig()

    def build_search_deployment_config(self) -> SearchDeploymentConfig:
        # Sharded defaults: 2 mongots/shard, sharded LB yaml, envoy port 27028.
        return SearchDeploymentConfig(
            mdbs_fixture_yaml="search-sharded-managed-lb.yaml",
            mongot_replicas=2,
            envoy_proxy_port=27028,
        )

    def build_sample_data_and_index_config(self) -> SampleDataAndIndexConfig:
        # Sharded inflates the corpus further (~70k total = 21k sample +
        # 50k synthetic) so cross-shard fanout paging tests can exercise
        # many more pages and cursor-loss tests have enough pages to fetch.
        return SampleDataAndIndexConfig(extra_doc_count=50_000)

    def effective_mdbs_resource_name(self) -> str:
        sd = self.build_search_deployment_config()
        if sd.mdbs_resource_name:
            return sd.mdbs_resource_name
        return self.build_mongodb_sharded_config().mdb_resource_name

    @fixture(scope="class")
    def ca_configmap(self, issuer_ca_filepath: str, namespace: str) -> str:
        cfg = self.build_mongodb_sharded_config()
        return create_issuer_ca(issuer_ca_filepath, namespace, cfg.ca_configmap_name)

    @fixture(scope="function")
    def helper(self, namespace: str) -> SearchDeploymentHelper:
        cfg = self.build_mongodb_sharded_config()
        return SearchDeploymentHelper(
            namespace=namespace,
            mdb_resource_name=cfg.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            shard_count=cfg.shard_count,
            mongods_per_shard=cfg.mongods_per_shard,
            mongos_count=cfg.mongos_count,
            config_server_count=cfg.config_server_count,
            ca_configmap_name=cfg.ca_configmap_name,
        )

    @fixture(scope="function")
    def mdb(self, namespace: str, ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
        cfg = self.build_mongodb_sharded_config()
        return helper.create_sharded_mdb(set_tls_ca=cfg.set_tls_ca)

    @fixture(scope="function")
    def mdbs(self, namespace: str) -> MongoDBSearch:
        sd_cfg = self.build_search_deployment_config()
        mongo_cfg = self.build_mongodb_sharded_config()
        resource = MongoDBSearch.from_yaml(
            yaml_fixture(sd_cfg.mdbs_fixture_yaml),
            namespace=namespace,
            name=self.effective_mdbs_resource_name(),
        )
        if try_load(resource):
            return resource
        resource["spec"]["source"]["mongodbResourceRef"]["name"] = mongo_cfg.mdb_resource_name
        resource["spec"]["replicas"] = sd_cfg.mongot_replicas
        return resource

    @fixture(scope="function")
    def admin_user(self, helper: SearchDeploymentHelper) -> MongoDBUser:
        cfg = self.build_mongodb_sharded_config()
        return helper.admin_user_resource(cfg.admin_user_name)

    @fixture(scope="function")
    def user(self, helper: SearchDeploymentHelper) -> MongoDBUser:
        cfg = self.build_mongodb_sharded_config()
        return helper.user_resource(cfg.user_name)

    @fixture(scope="function")
    def mongot_user(self, helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
        cfg = self.build_mongodb_sharded_config()
        return helper.mongot_user_resource(mdbs, cfg.mongot_user_name)

    @fixture(scope="module")
    def search_tools_pod_api_client(self):
        return None

    @fixture(scope="module")
    def search_tools_pod(self, namespace: str, search_tools_pod_api_client) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace, api_client=search_tools_pod_api_client)

    def _admin_tester(self, mdb: MongoDB) -> SearchTester:
        cfg = self.build_mongodb_sharded_config()
        return get_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password, use_ssl=True)

    def _user_tester(self, mdb: MongoDB) -> SearchTester:
        cfg = self.build_mongodb_sharded_config()
        return get_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)


class MongoDBShardedDeploymentTests(SearchShardedE2EFixtures):
    """Operator + sharded MDB + users bootstrap."""

    @skip_if_cloud_manager
    def test_create_ops_manager(self, namespace: str):
        ops_manager = get_ops_manager(namespace)
        assert ops_manager is not None
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_install_tls_certificates(self, helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
        helper.install_sharded_tls_certificates()

    def test_create_sharded_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_users(
        self,
        helper: SearchDeploymentHelper,
        admin_user: MongoDBUser,
        user: MongoDBUser,
        mongot_user: MongoDBUser,
    ):
        cfg = self.build_mongodb_sharded_config()
        helper.deploy_users(
            admin_user,
            cfg.admin_user_password,
            user,
            cfg.user_password,
            mongot_user,
            cfg.mongot_user_password,
        )


class SearchShardedDeploymentTests(SearchShardedE2EFixtures):
    """LB + per-shard mongot TLS + MongoDBSearch + envoy + mongod params."""

    def test_deploy_lb_certificates(self, namespace: str, issuer: str):
        sd_cfg = self.build_search_deployment_config()
        mongo_cfg = self.build_mongodb_sharded_config()
        create_lb_certificates(
            namespace,
            issuer,
            mongo_cfg.shard_count,
            mongo_cfg.mdb_resource_name,
            self.effective_mdbs_resource_name(),
            sd_cfg.mdbs_tls_cert_prefix,
        )

    def test_create_search_tls_certificate(self, namespace: str, issuer: str):
        sd_cfg = self.build_search_deployment_config()
        mongo_cfg = self.build_mongodb_sharded_config()
        create_per_shard_search_tls_certs(
            namespace,
            issuer,
            sd_cfg.mdbs_tls_cert_prefix,
            mongo_cfg.shard_count,
            mongo_cfg.mdb_resource_name,
            self.effective_mdbs_resource_name(),
        )

    def test_create_search_resource(self, mdbs: MongoDBSearch):
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=600)

    def test_verify_envoy_deployment(self, namespace: str):
        envoy_deployment_name = search_resource_names.lb_deployment_name(self.effective_mdbs_resource_name())

        def check_envoy_deployment():
            try:
                apps_v1 = client.AppsV1Api()
                deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
                ready = deployment.status.ready_replicas or 0
                return ready >= 1, f"ready_replicas={ready}"
            except Exception as e:
                return False, f"Deployment {envoy_deployment_name} not found: {e}"

        run_periodically(
            check_envoy_deployment, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_deployment_name}"
        )

    def test_wait_for_sharded_cluster_ready(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_verify_mongod_parameters_per_shard(self, namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
        sd_cfg = self.build_search_deployment_config()
        mongo_cfg = self.build_mongodb_sharded_config()
        verify_sharded_mongod_parameters(
            namespace,
            mongo_cfg.mdb_resource_name,
            mdbs.name,
            mongo_cfg.shard_count,
            expected_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
                mdbs.name, shard, namespace, sd_cfg.envoy_proxy_port
            ),
        )


class SearchShardedSampleDataAndIndex(SearchSampleDataAndIndexTests):
    """Sharded sample-data variant — same six test methods as the base,
    with ``_post_restore_setup`` overridden to shard + distribute the
    collection between restore (+synthetic corpus) and index creation.
    """

    def _post_restore_setup(self, mdb) -> None:
        sample_cfg = self.build_sample_data_and_index_config()
        self._admin_tester(mdb).shard_and_distribute_collection(
            sample_cfg.sample_database, sample_cfg.sample_collection
        )
