"""Reusable pytest test-class bases for the managed-LB search bootstrap."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import List, Optional

import kubernetes
from kubernetes import client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.mc_search_helper import patch_per_cluster_sharded_mongot_host_via_om
from tests.common.search.rs_search_helper import (
    create_rs_lb_certificates,
    create_rs_search_tls_cert,
    verify_rs_mongod_parameters,
)
from tests.common.search.search_deployment_helper import SearchDeploymentHelper, ensure_search_issuer
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
    verify_mongos_search_config,
    verify_sharded_mongod_parameters,
)
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_default_operator,
    get_issuer_ca_filepath,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
    is_multi_cluster,
)
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# The four search setParameters every mongod source needs, minus mongotHost
# (wired separately once the search proxy FQDN is known).
SEARCH_SET_PARAMETERS = {
    "skipAuthenticationToSearchIndexManagementServer": False,
    "skipAuthenticationToMongot": False,
    "searchTLSMode": "requireTLS",
    "useGrpcForSearch": True,
}


@dataclass
class MongoDBDeploymentConfig:
    """Flat external-source config holding RS + sharded + MC RS fields together."""

    mdb_resource_name: str = "mdb"

    admin_user_name: str = ""
    admin_user_password: str = ""
    user_name: str = ""
    user_password: str = ""
    mongot_user_name: str = "search-sync-source"
    mongot_user_password: str = "search-sync-source-user-password"

    # RS-specific
    rs_members: int = 3
    set_tls: bool = True

    # Sharded-specific
    shard_count: int = 2
    mongods_per_shard: int = 1
    mongos_count: int = 1
    config_server_count: int = 1
    set_tls_ca: bool = True

    # MC RS-specific (List[Optional[int]] matches cluster_spec_list's signature).
    members_per_cluster: List[Optional[int]] = field(default_factory=lambda: [1, 1, 1])
    source_cert_prefix: str = "clustercert"

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

    @property
    def source_bundle_secret(self) -> str:
        return f"{self.source_cert_prefix}-{self.mdb_resource_name}-cert"


@dataclass
class SearchDeploymentConfig:
    """MongoDBSearch-side knobs, independent of the external source MongoDB."""

    mdbs_resource_name: Optional[str] = None
    tls_cert_prefix: str = "certs"
    envoy_proxy_port: int = 27028
    mongot_replicas: int = 2
    create_timeout: int = 600
    envoy_lb_replicas: int = 2
    mongot_cpu: str = "1"
    mongot_memory: str = "2Gi"
    mongot_cpu_request: str = "500m"

    def mongot_resource_requirements(self) -> dict:
        """Per-cluster mongot resourceRequirements. Without this the operator defaults
        to requests of 2 CPU / 4Gi per mongot pod, which exhausts a kind node."""
        return {
            "requests": {"cpu": self.mongot_cpu_request, "memory": self.mongot_memory},
            "limits": {"cpu": self.mongot_cpu, "memory": self.mongot_memory},
        }


@dataclass
class SampleDataAndIndexConfig:
    search_index_name: str = "default"
    smoke_query_text: str = "Apollo"
    smoke_query_path: str = "title"
    sample_database: str = "sample_mflix"
    sample_collection: str = "movies"
    extra_doc_count: int = 10_000
    extra_doc_batch_size: int = 1000


def _wait_for_envoy_deployment_ready(namespace: str, deployment_name: str) -> None:
    """Block until the (single-cluster) Envoy Deployment has >=1 ready replica."""

    def check():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment(deployment_name, namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"ready_replicas={ready}"
        except Exception as e:
            return False, f"Deployment {deployment_name} not found: {e}"

    run_periodically(check, timeout=120, sleep_time=5, msg=f"Envoy Deployment {deployment_name}")


class InstallOperatorTests:
    def test_install_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        if not is_multi_cluster():
            operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
        else:
            operator = get_multi_cluster_operator(
                namespace,
                central_cluster_name=get_central_cluster_name(),
                multi_cluster_operator_installation_config=get_multi_cluster_operator_installation_config(namespace),
                central_cluster_client=get_central_cluster_client(),
                member_cluster_clients=get_member_cluster_clients(),
                member_cluster_names=get_member_cluster_names(),
            )
        operator.wait_for_operator_ready()


class MongoDBSourceDeploymentTests:
    mdb_config: MongoDBDeploymentConfig = MongoDBDeploymentConfig()
    search_config: SearchDeploymentConfig = SearchDeploymentConfig()
    # Set on the concrete class: ``namespace = NAMESPACE``.
    namespace: str = ""
    # Topology trait: how long the source takes to reach Running.
    create_timeout: int = 300

    def effective_mdbs_resource_name(self) -> str:
        return self.search_config.mdbs_resource_name or self.mdb_config.mdb_resource_name

    def ensure_ca_configmap(self) -> str:
        return create_issuer_ca(get_issuer_ca_filepath(), self.namespace, self.mdb_config.ca_configmap_name)

    def users_api_client(self):
        """Cluster the MongoDBUser CRs + password secrets land in.

        SC leaves this None (operator-cluster default); MC overrides to the
        central-cluster client.
        """
        return None

    def user_helper(self) -> SearchDeploymentHelper:
        return SearchDeploymentHelper(
            namespace=self.namespace,
            mdb_resource_name=self.mdb_config.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            ca_configmap_name=self.mdb_config.ca_configmap_name,
            api_client=self.users_api_client(),
        )

    def deploy_source_users(self) -> None:
        # Shared across SC + MC: the helper's api_client (from users_api_client)
        # decides which cluster the users land in.
        helper = self.user_helper()
        admin_user = helper.admin_user_resource(self.mdb_config.admin_user_name)
        user = helper.user_resource(self.mdb_config.user_name)
        mongot_user = helper.mongot_user_resource(self.effective_mdbs_resource_name(), self.mdb_config.mongot_user_name)
        helper.deploy_users(
            admin_user,
            self.mdb_config.admin_user_password,
            user,
            self.mdb_config.user_password,
            mongot_user,
            self.mdb_config.mongot_user_password,
        )

    # --- hooks (topology overrides) ---

    def create_ops_manager(self) -> None:
        # Default: in-cluster OM bring-up. Skipped entirely under cloud-qa
        # (get_ops_manager returns None) via @skip_if_cloud_manager on the step.
        ops_manager = get_ops_manager(self.namespace)
        assert ops_manager is not None
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def build_source(self) -> MongoDB:
        raise NotImplementedError("topology Layer-1 base must implement build_source")

    def install_source_tls_certificates(self) -> None:
        raise NotImplementedError("topology Layer-1 base must implement install_source_tls_certificates")

    # --- test_ steps (definition order = execution order) ---

    @skip_if_cloud_manager
    def test_create_ops_manager(self):
        self.create_ops_manager()

    def test_install_source_tls_certificates(self):
        self.install_source_tls_certificates()

    def test_create_source(self):
        source = self.build_source()
        source.update()
        source.assert_reaches_phase(Phase.Running, timeout=self.create_timeout)

    def test_create_users(self):
        self.deploy_source_users()


class MongoDBRsDeploymentTests(MongoDBSourceDeploymentTests):
    """External ReplicaSet source."""

    def source_helper(self) -> SearchDeploymentHelper:
        return SearchDeploymentHelper(
            namespace=self.namespace,
            mdb_resource_name=self.mdb_config.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            tls_cert_prefix=self.search_config.tls_cert_prefix,
            ca_configmap_name=self.mdb_config.ca_configmap_name,
        )

    def build_source(self) -> MongoDB:
        self.ensure_ca_configmap()
        resource = self.source_helper().create_rs_mdb(set_tls=self.mdb_config.set_tls)
        resource["spec"]["additionalMongodConfig"] = {"setParameter": dict(SEARCH_SET_PARAMETERS)}
        return resource

    def install_source_tls_certificates(self) -> None:
        self.source_helper().install_rs_tls_certificates(
            ensure_search_issuer(self.namespace), members=self.mdb_config.rs_members
        )


class SearchDeploymentTests:
    """Search deploy over an external source with explicit mongotHost wiring."""

    mdb_config: MongoDBDeploymentConfig = MongoDBDeploymentConfig()
    search_config: SearchDeploymentConfig = SearchDeploymentConfig()
    # Set on the concrete class: ``namespace = NAMESPACE``.
    namespace: str = ""

    def effective_mdbs_resource_name(self) -> str:
        return self.search_config.mdbs_resource_name or self.mdb_config.mdb_resource_name

    # --- hooks (topology overrides) ---

    def build_mdbs(self) -> MongoDBSearch:
        raise NotImplementedError

    def deploy_lb_certificates(self) -> None:
        raise NotImplementedError

    def create_search_tls_certificate(self) -> None:
        raise NotImplementedError

    def verify_search_deployment(self) -> None:
        raise NotImplementedError

    def wire_mongot_host(self) -> None:
        raise NotImplementedError

    def verify_mongod_parameters(self) -> None:
        raise NotImplementedError

    # --- test_ steps (definition order = execution order) ---

    def test_deploy_lb_certificates(self):
        self.deploy_lb_certificates()

    def test_create_search_tls_certificate(self):
        self.create_search_tls_certificate()

    def create_search_resource(self, wait: bool = True) -> MongoDBSearch:
        """Apply the MongoDBSearch spec. With ``wait`` (default) block until Running;
        ``wait=False`` returns right after update() so a caller can observe the
        not-yet-ready window."""
        mdbs = self.build_mdbs()
        mdbs.update()
        if wait:
            mdbs.assert_reaches_phase(Phase.Running, timeout=self.search_config.create_timeout)
        return mdbs

    def test_create_search_resource(self):
        self.create_search_resource()

    def test_verify_search_deployment(self):
        self.verify_search_deployment()

    def test_wire_mongot_host(self):
        self.wire_mongot_host()

    def test_verify_mongod_parameters(self):
        self.verify_mongod_parameters()


class SearchRsDeploymentTests(SearchDeploymentTests):
    """SC ReplicaSet search deploy. SC = one cluster entry, no name (index 0)."""

    def search_clusters(self) -> list:
        return [
            {
                "replicas": self.search_config.mongot_replicas,
                "resourceRequirements": self.search_config.mongot_resource_requirements(),
            }
        ]

    def build_mdbs(self) -> MongoDBSearch:
        helper = SearchDeploymentHelper(
            namespace=self.namespace,
            mdb_resource_name=self.mdb_config.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            tls_cert_prefix=self.search_config.tls_cert_prefix,
            ca_configmap_name=self.mdb_config.ca_configmap_name,
        )
        return helper.mdbs_for_ext_rs_source(
            self.mdb_config.mongot_user_name,
            members=self.mdb_config.rs_members,
            lb_mode="Managed",
            clusters=self.search_clusters(),
        )

    def deploy_lb_certificates(self) -> None:
        create_rs_lb_certificates(
            self.namespace,
            ensure_search_issuer(self.namespace),
            self.effective_mdbs_resource_name(),
            self.search_config.tls_cert_prefix,
        )

    def create_search_tls_certificate(self) -> None:
        # Index-0 headless-svc SAN: the operator deploys the RS mongot STS at index 0.
        indexed_svc = search_resource_names.mongot_service_name_for_cluster(self.effective_mdbs_resource_name())
        create_rs_search_tls_cert(
            self.namespace,
            ensure_search_issuer(self.namespace),
            self.effective_mdbs_resource_name(),
            self.search_config.tls_cert_prefix,
            extra_domains=[f"{indexed_svc}.{self.namespace}.svc.cluster.local"],
        )

    def verify_search_deployment(self) -> None:
        _wait_for_envoy_deployment_ready(
            self.namespace, search_resource_names.lb_deployment_name(self.effective_mdbs_resource_name())
        )

    def wire_mongot_host(self) -> None:
        host = search_resource_names.proxy_service_host(
            self.effective_mdbs_resource_name(), self.namespace, self.search_config.envoy_proxy_port
        )
        mdb = MongoDB(name=self.mdb_config.mdb_resource_name, namespace=self.namespace)
        mdb.load()
        mdb["spec"]["additionalMongodConfig"] = {
            "setParameter": {"mongotHost": host, "searchIndexManagementHostAndPort": host, **SEARCH_SET_PARAMETERS}
        }
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def verify_mongod_parameters(self) -> None:
        expected_host = search_resource_names.proxy_service_host(
            self.effective_mdbs_resource_name(), self.namespace, self.search_config.envoy_proxy_port
        )
        verify_rs_mongod_parameters(
            self.namespace, self.mdb_config.mdb_resource_name, self.mdb_config.rs_members, expected_host
        )


class SearchSampleDataAndIndexTests:
    """Restore sample data, inflate, build index, smoke-query — name-based testers."""

    sample_config: SampleDataAndIndexConfig = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str) -> SearchTester:
        raise NotImplementedError("concrete TestSampleData must build an admin tester")

    def user_tester(self, namespace: str) -> SearchTester:
        raise NotImplementedError("concrete TestSampleData must build a user tester")

    def post_restore_setup(self, namespace: str) -> None:
        return None

    def tools_pod_api_client(self):
        return None

    @fixture(scope="module")
    def search_tools_pod(self, namespace: str) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace, api_client=self.tools_pod_api_client())

    def test_deploy_tools_pod(self, search_tools_pod: mongodb_tools_pod.ToolsPod):
        logger.info(f"Tools pod {search_tools_pod.pod_name} is ready")

    def test_restore_sample_database(self, namespace: str, search_tools_pod: mongodb_tools_pod.ToolsPod):
        # mongorestore --drop recreates the collection (new UUID) and orphans mongot's
        # search index, so skip the restore when the sample data is already present.
        db, coll = self.sample_config.sample_database, self.sample_config.sample_collection
        tester = self.admin_tester(namespace)
        if tester.client[db][coll].count_documents({"synthetic": {"$ne": True}}, limit=1) > 0:
            logger.info(f"sample data already present in {db}.{coll}; skipping mongorestore")
            return
        tester.mongorestore_from_url(
            archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
            ns_include=f"{self.sample_config.sample_database}.*",
            tools_pod=search_tools_pod,
        )

    def test_insert_synthetic_corpus(self, namespace: str):
        if self.sample_config.extra_doc_count <= 0:
            logger.info("synthetic corpus inflation disabled (extra_doc_count=0)")
            return
        self.admin_tester(namespace).insert_synthetic_movies(
            self.sample_config.sample_database,
            self.sample_config.sample_collection,
            self.sample_config.extra_doc_count,
            batch_size=self.sample_config.extra_doc_batch_size,
        )

    def test_post_restore_setup(self, namespace: str):
        self.post_restore_setup(namespace)

    def test_create_search_index(self, namespace: str):
        tester = self.user_tester(namespace)
        db, coll = self.sample_config.sample_database, self.sample_config.sample_collection
        name = self.sample_config.search_index_name
        existing = {i.get("name") for i in tester.client[db][coll].aggregate([{"$listSearchIndexes": {}}])}
        if name in existing:
            logger.info(f"search index {name!r} already exists on {db}.{coll}; skipping create")
        else:
            tester.create_search_index(db, coll)
        tester.wait_for_search_indexes_ready(db, coll, timeout=300)

    def test_smoke_search_query_succeeds(self, namespace: str):
        tester = self.user_tester(namespace)
        pipeline = [
            {
                "$search": {
                    "index": self.sample_config.search_index_name,
                    "text": {"query": self.sample_config.smoke_query_text, "path": self.sample_config.smoke_query_path},
                }
            },
            {"$limit": 5},
        ]
        results = list(
            tester.client[self.sample_config.sample_database][self.sample_config.sample_collection].aggregate(pipeline)
        )
        assert results, (
            f"smoke $search against {self.sample_config.sample_database}.{self.sample_config.sample_collection} "
            f"returned 0 docs — deployment is not wired"
        )


class MongoDBShardedDeploymentTests(MongoDBSourceDeploymentTests):
    """External sharded source."""

    create_timeout: int = 900

    def source_helper(self) -> SearchDeploymentHelper:
        return SearchDeploymentHelper(
            namespace=self.namespace,
            mdb_resource_name=self.mdb_config.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            shard_count=self.mdb_config.shard_count,
            mongods_per_shard=self.mdb_config.mongods_per_shard,
            mongos_count=self.mdb_config.mongos_count,
            config_server_count=self.mdb_config.config_server_count,
            ca_configmap_name=self.mdb_config.ca_configmap_name,
        )

    def build_source(self) -> MongoDB:
        self.ensure_ca_configmap()
        resource = self.source_helper().create_sharded_mdb(set_tls_ca=self.mdb_config.set_tls_ca)
        search_set_parameter = {"setParameter": dict(SEARCH_SET_PARAMETERS)}
        resource["spec"].setdefault("shard", {})["additionalMongodConfig"] = search_set_parameter
        resource["spec"].setdefault("mongos", {})["additionalMongodConfig"] = search_set_parameter
        return resource

    def install_source_tls_certificates(self) -> None:
        # ensure_search_issuer installs cert-manager + creates the per-namespace
        # ca-issuer Issuer (the transitive dep the old `issuer` fixture provided).
        # create_sharded_cluster_certs references ca-issuer by default, so this must
        # run first — the RS path passes it explicitly; the sharded path relied on it.
        ensure_search_issuer(self.namespace)
        self.source_helper().install_sharded_tls_certificates()


class SearchShardedDeploymentTests(SearchDeploymentTests):
    """SC sharded search deploy with explicit per-(cluster,shard) mongotHost wiring."""

    def search_clusters(self) -> list:
        return [
            {
                "replicas": self.search_config.mongot_replicas,
                "resourceRequirements": self.search_config.mongot_resource_requirements(),
            }
        ]

    def cluster_indexes(self) -> List[int]:
        return [0]

    def search_api_client(self):
        """Cluster the MongoDBSearch CR lives in. SC: None (default/operator client);
        MC overrides to the central-cluster client."""
        return None

    def shard_names(self) -> List[str]:
        return [f"{self.mdb_config.mdb_resource_name}-{i}" for i in range(self.mdb_config.shard_count)]

    def source_router_hosts(self) -> List[str]:
        mdb_name = self.mdb_config.mdb_resource_name
        return [
            f"{mdb_name}-mongos-{i}.{mdb_name}-svc.{self.namespace}.svc.cluster.local:27017"
            for i in range(self.mdb_config.mongos_count)
        ]

    def source_shards(self) -> list:
        mdb_name = self.mdb_config.mdb_resource_name
        return [
            {
                "shardName": shard_name,
                "hosts": [
                    f"{shard_name}-{m}.{mdb_name}-sh.{self.namespace}.svc.cluster.local:27017"
                    for m in range(self.mdb_config.mongods_per_shard)
                ],
            }
            for shard_name in self.shard_names()
        ]

    def build_mdbs(self) -> MongoDBSearch:
        mdbs_name = self.effective_mdbs_resource_name()
        resource = MongoDBSearch.from_yaml(
            yaml_fixture("search-sharded-external-mongod.yaml"),
            namespace=self.namespace,
            name=mdbs_name,
        )
        api_client = self.search_api_client()
        if api_client is not None:
            resource.api = kubernetes.client.CustomObjectsApi(api_client)
        # Load if it exists so update() patches in place, but always rebuild the spec so a
        # grown shard_count (e.g. after adding a shard) is reflected.
        try_load(resource)
        resource["spec"]["source"] = {
            "username": self.mdb_config.mongot_user_name,
            "passwordSecretRef": {
                "name": f"{mdbs_name}-{self.mdb_config.mongot_user_name}-password",
                "key": "password",
            },
            "external": {
                "shardedCluster": {
                    "router": {"hosts": self.source_router_hosts()},
                    "shards": self.source_shards(),
                },
                "tls": {"ca": {"name": self.mdb_config.ca_configmap_name}},
            },
        }
        resource["spec"]["security"] = {"tls": {"certsSecretPrefix": self.search_config.tls_cert_prefix}}
        clusters = self.search_clusters()
        for i, cluster in enumerate(clusters):
            cluster["loadBalancer"] = {
                "managed": {
                    "externalHostname": search_resource_names.shard_proxy_svc_hostname_template(
                        mdbs_name, self.namespace, i
                    ),
                    # Shard-agnostic cluster-level endpoint for mongos: the per-cluster proxy-svc FQDN
                    # (matches the LB cert SAN). Required for external sharded + managed LB.
                    "routerHostname": search_resource_names.mc_proxy_svc_fqdn(mdbs_name, self.namespace, i),
                },
            }
        resource["spec"]["clusters"] = clusters
        return resource

    def deploy_lb_certificates(self) -> None:
        create_lb_certificates(
            self.namespace,
            ensure_search_issuer(self.namespace),
            self.mdb_config.shard_count,
            self.mdb_config.mdb_resource_name,
            self.effective_mdbs_resource_name(),
            self.search_config.tls_cert_prefix,
            cluster_indexes=self.cluster_indexes(),
        )

    def create_search_tls_certificate(self) -> None:
        for cluster_index in self.cluster_indexes():
            create_per_shard_search_tls_certs(
                self.namespace,
                ensure_search_issuer(self.namespace),
                self.search_config.tls_cert_prefix,
                self.mdb_config.shard_count,
                self.mdb_config.mdb_resource_name,
                self.effective_mdbs_resource_name(),
                cluster_index=cluster_index,
            )

    def verify_search_deployment(self) -> None:
        _wait_for_envoy_deployment_ready(
            self.namespace, search_resource_names.lb_deployment_name(self.effective_mdbs_resource_name())
        )

    def wire_mongot_host(self) -> None:
        mdb = MongoDB(name=self.mdb_config.mdb_resource_name, namespace=self.namespace)
        mdb.load()
        patch_per_cluster_sharded_mongot_host_via_om(
            mdb=mdb,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            namespace=self.namespace,
            shard_count=self.mdb_config.shard_count,
            cluster_indexes=self.cluster_indexes(),
            envoy_proxy_port=self.search_config.envoy_proxy_port,
            multi_cluster=len(self.cluster_indexes()) > 1,
        )
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def verify_mongod_parameters(self) -> None:
        mdbs_name = self.effective_mdbs_resource_name()
        verify_sharded_mongod_parameters(
            self.namespace,
            self.mdb_config.mdb_resource_name,
            mdbs_name,
            self.mdb_config.shard_count,
            expected_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
                mdbs_name, shard, self.namespace, self.search_config.envoy_proxy_port
            ),
        )
        verify_mongos_search_config(self.namespace, self.mdb_config.mdb_resource_name)


class SearchShardedSampleDataAndIndex(SearchSampleDataAndIndexTests):
    """Sharded sample-data variant — shard + distribute the collection in post-restore."""

    def post_restore_setup(self, namespace: str) -> None:
        self.admin_tester(namespace).shard_and_distribute_collection(
            self.sample_config.sample_database, self.sample_config.sample_collection
        )
