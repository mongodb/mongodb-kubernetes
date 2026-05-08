"""Reusable pytest test-class mixins for the managed-LB search bootstrap.

The "deploy operator → deploy MongoDB → deploy MongoDBSearch → load
sample data → build search index" sequence is shared across the
connectivity-tool, background-tester, and failure-modes search e2es,
and across all three topologies (SC RS, SC sharded, MC RS).

Layer 1 (database install) and Layer 2 (MongoDBSearch + envoy install)
diverge per topology — different CRDs, different cert plumbing, different
mongod-parameter verifications — so they stay per-topology:

* ``MongoDBRsDeploymentTests`` / ``SearchDeploymentTests`` — SC RS.
* ``MongoDBShardedDeploymentTests`` / ``SearchShardedDeploymentTests`` — SC sharded.

Layer 3 (restore + index + smoke) is topology-agnostic:

* ``SearchSampleDataAndIndexTests`` — pure mixin (no parent). Test bodies
  call ``self._admin_tester(mdb)`` / ``self._user_tester(mdb)`` /
  ``self.search_tools_pod``; topology fixtures classes
  (``SearchE2EFixtures`` / ``SearchShardedE2EFixtures``) supply those hooks.
* ``SearchShardedSampleDataAndIndex`` — same six test methods inherited
  from the base, with ``_post_restore_setup`` overridden to shard +
  distribute the sample collection between restore and index creation.

Each layer owns a ``@dataclass`` config object and a ``build_*_config()``
method. Test bodies and fixtures pull values out of the config returned
by ``self.build_*_config()`` — no class attribute surface, no fixture
wrappers around the config.

Consumers extend defaults the standard Python way:

    class _ConnToolRsConfig:
        def build_mongodb_rs_config(self):
            cfg = super().build_mongodb_rs_config()
            cfg.mdb_resource_name = "mdb-rs-foo"
            return cfg

**Consumer pattern — DECLARE BASES IN REVERSE EXECUTION ORDER:**

    pytestmark = pytest.mark.e2e_<file_specific_marker>

    class TestBootstrap(
        _ConnToolRsConfig,
        SearchDeploymentTests,        # Layer 2 — runs second
        MongoDBRsDeploymentTests,     # Layer 1 — runs FIRST
    ):
        pass

    class TestSampleData(
        _ConnToolRsConfig,
        SearchSampleDataAndIndexTests,   # pure mixin — needs topology fixtures
        SearchE2EFixtures,               # supplies hooks consumed by Layer 3
    ):
        pass

    class TestUnique(_ConnToolRsConfig, SearchE2EFixtures):
        def test_unique_scenario(self, mdb, mdbs, namespace):
            ...

Why bases are listed in reverse: pytest's class collector
(``_pytest.python.PyCollector.collect``) emits inherited test methods
in ``reversed(MRO)`` order. So the FIRST base in the declaration is
emitted LAST. Declaring bases in reverse-execution order makes the
bootstrap fire MongoDB → Search → Data → unique-scenarios.

**Redundant-base rule:** include a topology fixtures base
(``SearchE2EFixtures`` / ``SearchShardedE2EFixtures``) in the consumer's
bases ONLY when no layer mixin in the same declaration already inherits
from it. Layer 1/2
mixins extend their topology fixtures class transitively, so adding
it again is noise. Layer 3 (``SearchSampleDataAndIndexTests``) is a
pure mixin and DOES require an explicit topology fixtures base.

The mixins carry NO ``@mark.e2e_*`` decorators. Marks are applied at
the module level (``pytestmark = pytest.mark.e2e_<marker>``) so they
attach to every collected class without duplication.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

from kubernetes import client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture
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
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)


# ---------------------------------------------------------------------------
# Per-layer configuration dataclasses. Subclasses override the matching
# ``build_*_config`` method below to mutate fields via ``super()``.
# ---------------------------------------------------------------------------


def _derive_user_defaults(cfg) -> None:
    """Fill ``admin_user_name``/``user_name`` (+ passwords) from ``mdb_resource_name``.

    Two e2e tests that share a namespace (or a leftover-from-yesterday
    namespace) used to fight over a single ``mdb-admin-user`` /
    ``mdb-user`` MongoDBUser CR. The first test to apply it would win
    the ``mongodbResourceRef``; the second's bootstrap would either time
    out waiting for ``Updated`` or auth would fail against the wrong
    cluster. Deriving every user name from ``mdb_resource_name`` makes
    each test's user set unique by construction. Explicit overrides
    (set on the dataclass before ``__post_init__`` runs, e.g. via
    ``replace(cfg, admin_user_name="...")``) still win.
    """
    if not cfg.admin_user_name:
        cfg.admin_user_name = f"{cfg.mdb_resource_name}-admin-user"
    if not cfg.admin_user_password:
        cfg.admin_user_password = f"{cfg.admin_user_name}-pass"
    if not cfg.user_name:
        cfg.user_name = f"{cfg.mdb_resource_name}-user"
    if not cfg.user_password:
        cfg.user_password = f"{cfg.user_name}-pass"


@dataclass
class MongoDBRsDeploymentConfig:
    """MongoDB replica set + operator + users."""

    mdb_resource_name: str = "mdb-rs"
    rs_members: int = 3
    set_tls: bool = True

    # User names/passwords are derived from ``mdb_resource_name`` via
    # ``__post_init__`` when left unset, so two tests sharing a namespace
    # don't collide on a single ``mdb-admin-user`` MongoDBUser CR.
    admin_user_name: str = ""
    admin_user_password: str = ""
    user_name: str = ""
    user_password: str = ""
    mongot_user_name: str = "search-sync-source"
    mongot_user_password: str = "search-sync-source-user-password"

    def __post_init__(self) -> None:
        _derive_user_defaults(self)

    @property
    def ca_configmap_name(self) -> str:
        return f"{self.mdb_resource_name}-ca"


@dataclass
class SearchDeploymentConfig:
    """MongoDBSearch CR + envoy.

    ``mdbs_resource_name`` defaults to ``None``; the fixtures resolve
    it to the matching MongoDB resource name if unset.

    ``mongot_replicas`` is the per-shard mongot count for sharded
    deployments; the RS fixture leaves the yaml-default in place.
    """

    mdbs_resource_name: Optional[str] = None
    mdbs_tls_cert_prefix: str = "certs"
    mdbs_fixture_yaml: str = "search-rs-managed-lb.yaml"
    envoy_proxy_port: int = 27028
    mongot_replicas: int = 1


@dataclass
class SampleDataAndIndexConfig:
    """Sample dataset, search index, smoke query.

    ``extra_doc_count`` > 0 triggers a synthetic doc insert AFTER
    mongorestore (+ after ``shard_and_distribute_collection`` for the
    sharded subclass), BEFORE ``create_search_index``. Default is
    ``10_000`` — enough corpus that paging tests behave consistently
    across SC RS / MC RS / failure-mode tests. The sharded topology
    overrides to ``50_000`` for cross-shard fanout paging tests.
    """

    search_index_name: str = "default"
    smoke_query_text: str = "Apollo"
    smoke_query_path: str = "title"
    sample_database: str = "sample_mflix"
    sample_collection: str = "movies"
    extra_doc_count: int = 10_000
    extra_doc_batch_size: int = 1000


# ---------------------------------------------------------------------------
# Hook contract shared by topology fixtures classes and the sample-data
# test mixin. The diamond inheritance through this class is what lets a
# topology-agnostic mixin call ``self.build_sample_data_and_index_config()``
# / ``self._admin_tester(mdb)`` / ``self._user_tester(mdb)`` while type
# checkers see the signatures from a real parent class. At runtime the
# consumer's MRO routes each call to whichever topology fixtures class is
# also a base of the same consumer.
# ---------------------------------------------------------------------------


class SearchE2EHooks:
    """Hook contract shared by every topology fixtures class and the
    sample-data test mixin.

    The defaults here cover only the configuration build: the tester
    factories must be supplied by a topology fixtures class
    (``SearchE2EFixtures`` / ``SearchShardedE2EFixtures``).
    """

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
    """Base class for the three layer mixins.

    Provides the six fixtures (``helper``, ``mdb``, ``mdbs``, users,
    ``ca_configmap``) and default ``build_*_config`` returning the
    dataclass defaults. Layer mixins inherit from this; their
    ``build_*_config`` overrides shadow the stubs.
    """

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


class MongoDBRsDeploymentTests(SearchE2EFixtures):
    """MongoDB replica-set deployment bootstrap.

    Operator install → optional OM → RS TLS certs → MongoDB resource
    (creates and waits Running) → users.
    """

    def test_install_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
        operator.assert_is_running()

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
    """Topology-agnostic sample-data + index + smoke test methods.

    Consumers combine with a topology fixtures class (``SearchE2EFixtures``
    or ``SearchShardedE2EFixtures``) which provides ``mdb``,
    ``search_tools_pod``, ``_admin_tester`` / ``_user_tester``, and the
    matching ``build_*_config``.

    Step ordering (source order = pytest collection order within a class):
    deploy tools → restore → synthetic corpus inflation →
    ``_post_restore_setup`` hook (no-op default on ``SearchE2EHooks``;
    ``SearchShardedSampleDataAndIndex`` overrides to shard + distribute) →
    create index → smoke query.

    Inherits the hook contract from ``SearchE2EHooks`` so type checkers
    resolve ``self.build_sample_data_and_index_config`` /
    ``self._admin_tester`` / ``self._user_tester``. At runtime Python's
    C3 MRO routes those calls through the consumer's topology fixtures
    base (which provides the concrete implementations) — the
    ``SearchE2EHooks`` body is the fallback for ``build_*`` defaults.
    """

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
        tester.wait_for_search_indexes_ready(
            sample_cfg.sample_database, sample_cfg.sample_collection, timeout=300
        )

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
        _derive_user_defaults(self)

    @property
    def ca_configmap_name(self) -> str:
        return f"{self.mdb_resource_name}-ca"


class SearchShardedE2EFixtures(SearchE2EHooks):
    """Sharded topology fixtures + tester factories."""

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
        # many more pages and any cursor-loss propagation gap on the
        # per-getMore gRPC path has more chances to surface.
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

    def test_install_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
        operator.assert_is_running()

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


# Backwards-compatible alias — existing imports of
# ``SearchShardedSampleDataAndIndexTests`` continue to work.
SearchShardedSampleDataAndIndexTests = SearchShardedSampleDataAndIndex
