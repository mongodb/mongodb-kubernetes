"""Reusable pytest test-class mixins for the RS managed-LB search bootstrap.

The 15-step "deploy operator → deploy MongoDB → deploy MongoDBSearch →
load sample data → build search index" sequence was duplicated almost
verbatim across the KUBE-17 connectivity-tool e2e, the KUBE-26
background-tester e2e, and the KUBE-27 failure-modes e2e. This module
splits each step into one of three sibling test-class mixins, all
parented at ``SearchE2EFixtures``:

* ``MongoDBRsDeploymentTests`` (Layer 1) — operator install, optional
  OM, RS TLS, MongoDB resource, users. Leaves MongoDB in ``Running``
  (pre-search).
* ``SearchDeploymentTests`` (Layer 2) — LB and search TLS, MongoDBSearch
  resource, envoy readiness, re-wait MongoDB to ``Running`` after
  mongod is reconciled with the search parameters, mongod-parameter
  verification.
* ``SearchSampleDataAndIndexTests`` (Layer 3) — tools pod, restore
  sample_mflix, create the search index, run a rudimentary search
  query as a smoke proof that the deployment is fully wired before any
  unique test runs.

Each layer owns a ``@dataclass`` config object
(``MongoDBRsDeploymentConfig`` / ``SearchDeploymentConfig`` /
``SampleDataAndIndexConfig``) and a plain ``build_*_config()`` method
returning the defaults. Test bodies and fixtures pull values out of
the config returned by ``self.build_*_config()`` — no class attribute
surface, no fixture wrappers around the config (the dataclass is
cheap to build; promoting to a fixture only pays off if it becomes
expensive or needs cross-fixture wiring).

Consumers extend defaults the standard Python way:

    class TestX(...):
        def build_mongodb_rs_config(self):
            cfg = super().build_mongodb_rs_config()
            cfg.mdb_resource_name = "mdb-rs-foo"
            return cfg

**Consumer pattern — DECLARE BASES IN REVERSE EXECUTION ORDER:**

    @mark.e2e_<file_specific_marker>
    class TestX(
        SearchSampleDataAndIndexTests,   # runs LAST
        SearchDeploymentTests,           # runs second
        MongoDBRsDeploymentTests,        # runs FIRST
        SearchE2EFixtures,
    ):
        def build_mongodb_rs_config(self):
            cfg = super().build_mongodb_rs_config()
            cfg.mdb_resource_name = "mdb-rs-foo"
            return cfg

        def test_unique_scenario(self, mdb, mdbs, namespace):
            ...

Why bases are listed in reverse: pytest's class collector
(``_pytest.python.PyCollector.collect``) emits inherited test methods
in ``reversed(MRO)`` order — explicit comment in the source: *"Between
classes in the class hierarchy, reverse-MRO order — nodes inherited
from base classes should come before subclasses."* So the FIRST base
in the declaration is emitted LAST, and so on. Declaring bases in
reverse-execution order makes the bootstrap fire MongoDB → Search →
Data → unique-scenarios, even though the source reads top-to-bottom
as Layer3 → Layer2 → Layer1 → consumer.

The base mixins carry NO ``@mark.e2e_*`` decorators. Marks are applied
only at the consuming subclass via the class-level pytestmark. That
keeps ``pytest -m <marker>`` from cross-collecting bootstrap methods
when the same mixin is reused in a sibling test file.

A consuming class that needs to swap one layer for a sibling variant
(e.g. internal-MongoDB instead of external RS) just substitutes that
one base in the declaration. The other layers stay intact.
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
from tests.common.search import search_resource_names
from tests.common.search.rs_search_helper import (
    create_rs_lb_certificates,
    create_rs_search_tls_cert,
    get_rs_search_tester,
    verify_rs_mongod_parameters,
)
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)


# ---------------------------------------------------------------------------
# Per-layer configuration dataclasses. Subclasses override the matching
# ``build_*_config`` method below to mutate fields via ``super()``.
# ---------------------------------------------------------------------------


@dataclass
class MongoDBRsDeploymentConfig:
    """Layer 1 — MongoDB replica set + operator + users."""

    mdb_resource_name: str = "mdb-rs"
    rs_members: int = 3
    set_tls: bool = True

    admin_user_name: str = "mdb-admin-user"
    admin_user_password: str = "mdb-admin-user-pass"
    user_name: str = "mdb-user"
    user_password: str = "mdb-user-pass"
    mongot_user_name: str = "search-sync-source"
    mongot_user_password: str = "search-sync-source-user-password"

    @property
    def ca_configmap_name(self) -> str:
        return f"{self.mdb_resource_name}-ca"


@dataclass
class SearchDeploymentConfig:
    """Layer 2 — MongoDBSearch CR + envoy.

    ``mdbs_resource_name`` defaults to ``None``; the fixtures resolve
    it to ``MongoDBRsDeploymentConfig.mdb_resource_name`` if unset,
    matching the convention that the search CR shares the source
    MongoDB's name. Override explicitly to break that link.
    """

    mdbs_resource_name: Optional[str] = None
    mdbs_tls_cert_prefix: str = "certs"
    mdbs_fixture_yaml: str = "search-rs-managed-lb.yaml"
    envoy_proxy_port: int = 27028


@dataclass
class SampleDataAndIndexConfig:
    """Layer 3 — sample dataset, search index, smoke query."""

    search_index_name: str = "default"
    smoke_query_text: str = "Apollo"
    smoke_query_path: str = "title"
    sample_database: str = "sample_mflix"
    sample_collection: str = "movies"


# ---------------------------------------------------------------------------
# Fixtures + default ``build_*_config`` stubs. Layer mixins override the
# build methods; consumers extend them with ``super()``.
# ---------------------------------------------------------------------------


class SearchE2EFixtures:
    """Base class for the three layer mixins.

    Provides the six fixtures (``helper``, ``mdb``, ``mdbs``, users,
    ``ca_configmap``) and default ``build_*_config`` returning the
    dataclass defaults. Layer mixins inherit from this; their
    ``build_*_config`` overrides shadow the stubs.
    """

    # Default config-builder hooks. Layer mixins / consumers override.
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return MongoDBRsDeploymentConfig()

    def build_search_deployment_config(self) -> SearchDeploymentConfig:
        return SearchDeploymentConfig()

    def build_sample_data_and_index_config(self) -> SampleDataAndIndexConfig:
        return SampleDataAndIndexConfig()

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


# ---------------------------------------------------------------------------
# Layer test mixins. Each owns its config + bootstrap test methods.
# ---------------------------------------------------------------------------


class MongoDBRsDeploymentTests(SearchE2EFixtures):
    """Layer 1 — MongoDB replica-set deployment.

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
    """Layer 2 — Search deployment on top of an existing MongoDB.

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


class SearchSampleDataAndIndexTests(SearchE2EFixtures):
    """Layer 3 — Sample data, search index, and a rudimentary smoke query.

    Tools pod → mongorestore sample_mflix → create search index →
    one $search aggregation that has to round-trip through envoy →
    mongot to succeed. The smoke query proves the deployment is fully
    wired end-to-end before any unique scenario runs.
    """

    def test_deploy_tools_pod(self, tools_pod: mongodb_tools_pod.ToolsPod):
        logger.info(f"Tools pod {tools_pod.pod_name} is ready")

    def test_restore_sample_database(self, mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password, use_ssl=True)
        search_tester.mongorestore_from_url(
            archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
            ns_include="sample_mflix.*",
            tools_pod=tools_pod,
        )

    def test_create_search_index(self, mdb: MongoDB):
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        search_tester.create_search_index("sample_mflix", "movies")
        search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)

    def test_smoke_search_query_succeeds(self, mdb: MongoDB):
        mongo_cfg = self.build_mongodb_rs_config()
        sample_cfg = self.build_sample_data_and_index_config()
        search_tester = get_rs_search_tester(mdb, mongo_cfg.user_name, mongo_cfg.user_password, use_ssl=True)
        pipeline = [
            {
                "$search": {
                    "index": sample_cfg.search_index_name,
                    "text": {"query": sample_cfg.smoke_query_text, "path": sample_cfg.smoke_query_path},
                }
            },
            {"$limit": 5},
        ]
        results = list(
            search_tester.client[sample_cfg.sample_database][sample_cfg.sample_collection].aggregate(pipeline)
        )
        assert results, (
            f"rudimentary $search against {sample_cfg.sample_database}.{sample_cfg.sample_collection} "
            f"returned 0 docs — deployment is not wired"
        )
