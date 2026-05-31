"""MC-specific pytest mixins for the managed-LB search bootstrap.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import List, Optional

import kubernetes
from kubetester import create_or_update_secret, try_load
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod
from tests.common.search.bootstrap_test_mixins import SampleDataAndIndexConfig, SearchE2EHooks, _derive_user_defaults
from tests.common.search.mc_search_helper import (
    assert_per_cluster_mongot_host_observed,
    create_mc_lb_certificates,
    create_mc_mongot_tls_cert,
    patch_per_cluster_mongot_host_via_om,
    replicate_search_secrets_to_members,
    verify_per_cluster_envoy_deployment,
    verify_per_cluster_envoy_sni,
    verify_per_cluster_mongot_resources,
)
from tests.common.search.rs_search_helper import get_mc_rs_search_tester
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)


# ---------------------------------------------------------------------------
# Per-layer configuration dataclasses.
# ---------------------------------------------------------------------------


@dataclass
class MongoDBMultiRsDeploymentConfig:
    """MongoDBMulti RS + operator + users."""

    mdb_resource_name: str = "mdb-mc-rs"
    # Per-cluster member counts; List[Optional[int]] matches cluster_spec_list's
    # invariant signature. 1-per-cluster is the connectivity-tool default
    # (3-cluster topology with 1 mongod each).
    members_per_cluster: List[Optional[int]] = field(default_factory=lambda: [1, 1, 1])
    source_cert_prefix: str = "clustercert"

    # Derived from ``mdb_resource_name`` when unset — see
    # ``_derive_user_defaults`` in bootstrap_test_mixins.
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

    @property
    def source_bundle_secret(self) -> str:
        return f"{self.source_cert_prefix}-{self.mdb_resource_name}-cert"


@dataclass
class SearchMCDeploymentConfig:
    """MongoDBSearch CR + per-cluster envoy + AC mongotHost patch."""

    mdbs_resource_name: Optional[str] = None
    mdbs_tls_cert_prefix: str = "certs"
    mdbs_fixture_yaml: str = "search-q2-mc-rs-search.yaml"
    envoy_proxy_port: int = 27028
    mongot_replicas_per_cluster: int = 1
    envoy_lb_replicas: int = 2


# Sample-data config uses the shared ``SampleDataAndIndexConfig`` from
# ``bootstrap_test_mixins`` — the MC default (``extra_doc_count=10_000``)
# matches the shared default, so no MC-specific dataclass is needed.


# ---------------------------------------------------------------------------
# MC fixtures + default ``build_*_config`` stubs.
# ---------------------------------------------------------------------------


def _build_user(
    yaml_filename: str,
    name: str,
    username: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb_resource_name: str,
) -> MongoDBUser:
    """Single-source-of-truth MongoDBUser factory for the MC mixins.

    Mirrors ``q2_mc_rs_steady._build_user`` — wire the mongodbResourceRef
    name and password secret ref, keep the yaml-supplied roles intact.
    """
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


def _apply_user_password(
    user_resource: MongoDBUser,
    password: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> None:
    create_or_update_secret(
        namespace,
        name=user_resource["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": password},
        api_client=central_cluster_client,
    )
    user_resource.update()


class SearchMCE2EFixtures(SearchE2EHooks):
    """MC topology fixtures + tester factories."""

    # Default config-builder hooks. Consumers override via ``super()``.
    def build_mongodb_mc_rs_config(self) -> MongoDBMultiRsDeploymentConfig:
        return MongoDBMultiRsDeploymentConfig()

    def build_search_mc_deployment_config(self) -> SearchMCDeploymentConfig:
        return SearchMCDeploymentConfig()

    def effective_mdbs_resource_name(self) -> str:
        sd = self.build_search_mc_deployment_config()
        if sd.mdbs_resource_name:
            return sd.mdbs_resource_name
        return f"{self.build_mongodb_mc_rs_config().mdb_resource_name}-search"

    @fixture(scope="class")
    def ca_configmap(self, issuer_ca_filepath: str, namespace: str) -> str:
        cfg = self.build_mongodb_mc_rs_config()
        return create_issuer_ca(issuer_ca_filepath, namespace, cfg.ca_configmap_name)

    @fixture(scope="class")
    def helper(
        self,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
    ) -> MCSearchDeploymentHelper:
        mongo_cfg = self.build_mongodb_mc_rs_config()
        return MCSearchDeploymentHelper(
            namespace=namespace,
            mdb_resource_name=mongo_cfg.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            member_cluster_clients={mcc.cluster_name: mcc.core_v1_api() for mcc in member_cluster_clients},
        )

    # ``search_tools_pod`` lands in the first member cluster — the
    # operator cluster does not host any MongoDBMulti services, so its
    # DNS can't resolve ``{mdb}-0-0-svc.{ns}.svc.cluster.local``.

    @fixture(scope="module")
    def search_tools_pod_api_client(
        self,
        member_cluster_clients: List[MultiClusterClient],
    ) -> kubernetes.client.ApiClient:
        return member_cluster_clients[0].api_client

    @fixture(scope="module")
    def search_tools_pod(
        self,
        namespace: str,
        search_tools_pod_api_client: kubernetes.client.ApiClient,
    ) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace, api_client=search_tools_pod_api_client)

    def _admin_tester(self, mdb: MongoDBMulti) -> SearchTester:
        cfg = self.build_mongodb_mc_rs_config()
        return get_mc_rs_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password)

    def _user_tester(self, mdb: MongoDBMulti) -> SearchTester:
        cfg = self.build_mongodb_mc_rs_config()
        return get_mc_rs_search_tester(mdb, cfg.user_name, cfg.user_password)

    @fixture(scope="class")
    def mdb(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_names: List[str],
        ca_configmap: str,
    ) -> MongoDBMulti:
        """MongoDBMulti RS with TLS+SCRAM, no spec-level mongotHost.

        Why no ``mongotHost`` / ``searchIndexManagementHostAndPort`` here:
        setting them at the spec level makes the operator re-apply the same
        value to EVERY process on every reconcile (process.go mergeFrom +
        RemoveFieldsBasedOnDesiredAndPrevious), clobbering the per-cluster
        AC patch in ``test_patch_per_cluster_mongot_host``. Leaving them
        out keeps the per-process values stable across reconciles.

        ``searchTLSMode`` stays in spec — identical across all clusters, no
        per-cluster locality, no race risk.
        """
        cfg = self.build_mongodb_mc_rs_config()
        resource = MongoDBMulti.from_yaml(
            yaml_fixture("search-q2-mc-rs.yaml"),
            name=cfg.mdb_resource_name,
            namespace=namespace,
        )
        resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, cfg.members_per_cluster)
        resource["spec"]["additionalMongodConfig"] = {
            "setParameter": {
                "skipAuthenticationToSearchIndexManagementServer": False,
                "skipAuthenticationToMongot": False,
                "searchTLSMode": "requireTLS",
                "useGrpcForSearch": True,
            },
        }
        resource["spec"]["security"] = {
            "certsSecretPrefix": cfg.source_cert_prefix,
            "tls": {"ca": ca_configmap},
            "authentication": {"enabled": True, "modes": ["SCRAM"]},
        }
        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
        try_load(resource)
        return resource

    @fixture(scope="class")
    def mdbs(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
        mdb: MongoDBMulti,
        ca_configmap: str,
    ) -> MongoDBSearch:
        """MongoDBSearch over external MongoDBMulti source.

        ``spec.source.external.hostAndPorts`` seeds the top-level list into
        every cluster's mongot ConfigMap.
        ``spec.loadBalancer.managed.externalHostname`` uses ``{clusterIndex}``
        so per-cluster cert SANs, AC mongotHost values, and Envoy SNI all
        resolve to the same per-cluster proxy-svc FQDN.
        """
        sd_cfg = self.build_search_mc_deployment_config()
        mongo_cfg = self.build_mongodb_mc_rs_config()
        resource = MongoDBSearch.from_yaml(
            yaml_fixture(sd_cfg.mdbs_fixture_yaml),
            name=self.effective_mdbs_resource_name(),
            namespace=namespace,
        )
        seeds = [f"{svc}.{namespace}.svc.cluster.local:27017" for svc in mdb.service_names()]
        resource["spec"]["source"] = {
            "username": mongo_cfg.mongot_user_name,
            "passwordSecretRef": {
                "name": f"{self.effective_mdbs_resource_name()}-{mongo_cfg.mongot_user_name}-password",
                "key": "password",
            },
            "external": {
                "hostAndPorts": seeds,
                "tls": {"ca": {"name": ca_configmap}},
            },
        }
        resource["spec"]["security"] = {"tls": {"certsSecretPrefix": sd_cfg.mdbs_tls_cert_prefix}}
        resource["spec"]["loadBalancer"] = {
            "managed": {
                "externalHostname": (
                    f"{self.effective_mdbs_resource_name()}-search-{{clusterIndex}}-proxy-svc"
                    f".{namespace}.svc.cluster.local"
                ),
            },
        }
        # Cap each per-cluster mongot at 1 CPU so a 3-cluster MC search
        # deployment (mongot + envoy per cluster) fits on a multi-cluster
        # kind topology without CPU starvation. Operator default is 2 CPU.
        resource["spec"]["clusters"] = [
            {
                "clusterName": mcc.cluster_name,
                "replicas": sd_cfg.mongot_replicas_per_cluster,
                "resourceRequirements": {
                    "requests": {"cpu": "500m", "memory": "2Gi"},
                    "limits": {"cpu": "1", "memory": "2Gi"},
                },
            }
            for mcc in member_cluster_clients
        ]
        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
        try_load(resource)
        return resource

    @fixture(scope="class")
    def admin_user(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ) -> MongoDBUser:
        cfg = self.build_mongodb_mc_rs_config()
        return _build_user(
            "mongodbuser-mdb-admin.yaml",
            cfg.admin_user_name,
            cfg.admin_user_name,
            namespace,
            central_cluster_client,
            cfg.mdb_resource_name,
        )

    @fixture(scope="class")
    def user(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ) -> MongoDBUser:
        cfg = self.build_mongodb_mc_rs_config()
        return _build_user(
            "mongodbuser-mdb-user.yaml",
            cfg.user_name,
            cfg.user_name,
            namespace,
            central_cluster_client,
            cfg.mdb_resource_name,
        )

    @fixture(scope="class")
    def mongot_user(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ) -> MongoDBUser:
        cfg = self.build_mongodb_mc_rs_config()
        return _build_user(
            "mongodbuser-search-sync-source-user.yaml",
            f"{self.effective_mdbs_resource_name()}-{cfg.mongot_user_name}",
            cfg.mongot_user_name,
            namespace,
            central_cluster_client,
            cfg.mdb_resource_name,
        )


# ---------------------------------------------------------------------------
# MongoDBMulti RS deployment.
# ---------------------------------------------------------------------------


class MongoDBMultiRsDeploymentTests(SearchMCE2EFixtures):
    """Operator → source TLS → MongoDBMulti RS → users.

    Leaves the MongoDBMulti in Running phase (pre-search). Mongot user
    is created but not waited on — it needs ``searchCoordinator`` role
    from the MongoDBSearch CR, applied by the search-deployment mixin.
    """

    def test_install_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_install_source_tls_certificates(
        self,
        multi_cluster_issuer: str,
        mdb: MongoDBMulti,
        member_cluster_clients: List[MultiClusterClient],
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        cfg = self.build_mongodb_mc_rs_config()
        create_multi_cluster_mongodb_tls_certs(
            multi_cluster_issuer,
            cfg.source_bundle_secret,
            member_cluster_clients,
            central_cluster_client,
            mdb,
        )

    def test_create_mdb_resource(self, mdb: MongoDBMulti):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=1500)

    def test_create_user_credentials(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        admin_user: MongoDBUser,
        user: MongoDBUser,
        mongot_user: MongoDBUser,
    ):
        cfg = self.build_mongodb_mc_rs_config()

        _apply_user_password(admin_user, cfg.admin_user_password, namespace, central_cluster_client)
        admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

        _apply_user_password(user, cfg.user_password, namespace, central_cluster_client)
        user.assert_reaches_phase(Phase.Updated, timeout=300)

        # mongot user needs searchCoordinator role from the MongoDBSearch CR;
        # we don't wait here.
        _apply_user_password(mongot_user, cfg.mongot_user_password, namespace, central_cluster_client)


# ---------------------------------------------------------------------------
# MongoDBSearch CR + per-cluster envoy + AC mongotHost patch.
# ---------------------------------------------------------------------------


class SearchMCDeploymentTests(SearchMCE2EFixtures):
    """Per-cluster LB + mongot TLS → secret replication → MongoDBSearch
    → per-cluster shape verifications → per-cluster mongotHost AC patch
    → per-cluster envoy SNI assertion.

    Q2-style bootstrap checks (``verify_per_cluster_mongot_resources``,
    ``verify_per_cluster_envoy_deployment``, ``assert_per_cluster_mongot_host_observed``,
    ``verify_per_cluster_envoy_sni``) all run here — the connectivity-tool
    test gets them for free once it consumes this mixin.
    """

    def test_deploy_lb_certificates(
        self,
        namespace: str,
        multi_cluster_issuer: str,
        helper: MCSearchDeploymentHelper,
    ):
        sd_cfg = self.build_search_mc_deployment_config()
        create_mc_lb_certificates(
            namespace=namespace,
            issuer=multi_cluster_issuer,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            tls_cert_prefix=sd_cfg.mdbs_tls_cert_prefix,
            helper=helper,
            envoy_lb_replicas=sd_cfg.envoy_lb_replicas,
        )

    def test_create_search_tls_certificate(
        self,
        namespace: str,
        multi_cluster_issuer: str,
        helper: MCSearchDeploymentHelper,
    ):
        sd_cfg = self.build_search_mc_deployment_config()
        create_mc_mongot_tls_cert(
            namespace=namespace,
            issuer=multi_cluster_issuer,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            tls_cert_prefix=sd_cfg.mdbs_tls_cert_prefix,
            helper=helper,
        )

    def test_replicate_secrets_to_members(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
    ):
        sd_cfg = self.build_search_mc_deployment_config()
        mongo_cfg = self.build_mongodb_mc_rs_config()
        replicate_search_secrets_to_members(
            namespace=namespace,
            central_cluster_client=central_cluster_client,
            member_cluster_clients=member_cluster_clients,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            mdbs_tls_cert_prefix=sd_cfg.mdbs_tls_cert_prefix,
            mongot_user_name=mongo_cfg.mongot_user_name,
            ca_configmap_name=mongo_cfg.ca_configmap_name,
        )

    def test_create_search_resource(self, mdbs: MongoDBSearch):
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=900)

    def test_verify_per_cluster_mongot_resources(
        self,
        mdb: MongoDBMulti,
        namespace: str,
        helper: MCSearchDeploymentHelper,
        member_cluster_clients: List[MultiClusterClient],
    ):
        verify_per_cluster_mongot_resources(
            mdb=mdb,
            namespace=namespace,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            helper=helper,
            member_cluster_clients=member_cluster_clients,
        )

    def test_verify_per_cluster_envoy_deployment(
        self,
        namespace: str,
        helper: MCSearchDeploymentHelper,
        member_cluster_clients: List[MultiClusterClient],
    ):
        verify_per_cluster_envoy_deployment(
            namespace=namespace,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            helper=helper,
            member_cluster_clients=member_cluster_clients,
        )

    def test_verify_lb_status(self, mdbs: MongoDBSearch):
        mdbs.load()
        mdbs.assert_lb_status()

    def test_patch_per_cluster_mongot_host(
        self,
        mdb: MongoDBMulti,
        helper: MCSearchDeploymentHelper,
        member_cluster_clients: List[MultiClusterClient],
    ):
        sd_cfg = self.build_search_mc_deployment_config()
        patch_per_cluster_mongot_host_via_om(
            mdb=mdb,
            helper=helper,
            member_cluster_clients=member_cluster_clients,
            envoy_proxy_port=sd_cfg.envoy_proxy_port,
        )

    def test_per_cluster_mongot_host_observed(
        self,
        mdb: MongoDBMulti,
        helper: MCSearchDeploymentHelper,
        member_cluster_clients: List[MultiClusterClient],
    ):
        sd_cfg = self.build_search_mc_deployment_config()
        assert_per_cluster_mongot_host_observed(
            mdb=mdb,
            helper=helper,
            member_cluster_clients=member_cluster_clients,
            envoy_proxy_port=sd_cfg.envoy_proxy_port,
        )

    def test_per_cluster_envoy_sni_observed(
        self,
        namespace: str,
        helper: MCSearchDeploymentHelper,
        member_cluster_clients: List[MultiClusterClient],
    ):
        verify_per_cluster_envoy_sni(
            namespace=namespace,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            helper=helper,
            member_cluster_clients=member_cluster_clients,
        )


# Sample-data flow is shared — MC consumers use ``SearchSampleDataAndIndexTests``
# from ``bootstrap_test_mixins`` combined with ``SearchMCE2EFixtures``, which
# supplies the MC-flavoured ``_admin_tester`` / ``_user_tester`` /
# ``search_tools_pod`` hooks.
