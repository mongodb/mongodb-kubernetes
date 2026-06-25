"""MC-specific pytest bases for the managed-LB search bootstrap."""

from __future__ import annotations

from typing import List

import kubernetes
from kubetester import try_load
from kubetester.certs import create_tls_certs
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.multicluster.multicluster_utils import assert_deployment_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.bootstrap_test_mixins import (
    SEARCH_SET_PARAMETERS,
    MongoDBSourceDeploymentTests,
    SearchDeploymentTests,
    SearchShardedDeploymentTests,
)
from tests.common.search.mc_search_helper import (
    assert_per_cluster_mongot_host_observed,
    assert_sharded_mongot_host_observed,
    create_mc_lb_certificates,
    create_mc_mongot_tls_cert,
    patch_per_cluster_mongot_host_via_om,
    patch_per_cluster_sharded_mongot_host_via_om,
    replicate_search_secrets_to_members,
    replicate_sharded_search_secrets_to_members,
    verify_per_cluster_envoy_deployment,
    verify_per_cluster_envoy_sni,
    verify_per_cluster_mongot_resources,
)
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper, ensure_search_issuer
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
)
from tests.conftest import (
    get_central_cluster_client,
    get_issuer_ca_filepath,
    get_member_cluster_clients,
    get_member_cluster_names,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# Source-RS YAML (MongoDBMulti) + MongoDBSearch YAML shared by the MC RS tests.
MC_RS_SOURCE_FIXTURE = "search-q2-mc-rs.yaml"
MC_RS_SEARCH_FIXTURE = "search-q2-mc-rs-search.yaml"
# Source-sharded YAML (MongoDB, MultiCluster) shared by the MC sharded tests.
MC_SHARDED_SOURCE_FIXTURE = "search-q3-mc-sharded.yaml"


class MongoDBMultiRsDeploymentTests(MongoDBSourceDeploymentTests):
    """External MongoDBMulti RS source: TLS certs → MongoDBMulti (Running) → central-cluster users."""

    create_timeout: int = 1500

    def users_api_client(self) -> kubernetes.client.ApiClient:
        # MongoDBUser CRs for an MC source live in the central cluster.
        return get_central_cluster_client()

    def build_source(self) -> MongoDBMulti:
        # Spec-level mongotHost is omitted on purpose: the operator would re-apply
        # it to every process each reconcile, clobbering the per-cluster AC patch.
        self.ensure_ca_configmap()
        resource = MongoDBMulti.from_yaml(
            yaml_fixture(MC_RS_SOURCE_FIXTURE),
            name=self.mdb_config.mdb_resource_name,
            namespace=self.namespace,
        )
        resource["spec"]["clusterSpecList"] = cluster_spec_list(
            get_member_cluster_names(), self.mdb_config.members_per_cluster
        )
        resource["spec"]["additionalMongodConfig"] = {"setParameter": dict(SEARCH_SET_PARAMETERS)}
        resource["spec"]["security"] = {
            "certsSecretPrefix": self.mdb_config.source_cert_prefix,
            "tls": {"ca": self.mdb_config.ca_configmap_name},
            "authentication": {"enabled": True, "modes": ["SCRAM"]},
        }
        resource.api = kubernetes.client.CustomObjectsApi(get_central_cluster_client())
        try_load(resource)
        return resource

    def install_source_tls_certificates(self) -> None:
        create_multi_cluster_mongodb_tls_certs(
            ensure_search_issuer(self.namespace),
            self.mdb_config.source_bundle_secret,
            get_member_cluster_clients(),
            get_central_cluster_client(),
            self.build_source(),
        )


class SearchRsMcDeploymentTests(SearchDeploymentTests):
    """MC search deploy over the external MongoDBMulti source with per-cluster fan-out."""

    def deployment_helper(self) -> MCSearchDeploymentHelper:
        return MCSearchDeploymentHelper(
            namespace=self.namespace,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            member_cluster_clients={mcc.cluster_name: mcc.core_v1_api() for mcc in get_member_cluster_clients()},
        )

    def _load_source(self) -> MongoDBMulti:
        resource = MongoDBMulti(name=self.mdb_config.mdb_resource_name, namespace=self.namespace)
        resource.api = kubernetes.client.CustomObjectsApi(get_central_cluster_client())
        resource.load()
        return resource

    def search_clusters(self) -> list:
        return [
            {
                "name": cluster_name,
                "index": i,
                "replicas": self.search_config.mongot_replicas,
                "resourceRequirements": self.search_config.mongot_resource_requirements(),
            }
            for i, cluster_name in enumerate(get_member_cluster_names())
        ]

    def build_mdbs(self) -> MongoDBSearch:
        # MC source: MongoDBMulti per-pod FQDNs; each cluster's managed externalHostname
        # carries its own index so per-cluster cert SANs, AC mongotHost, and Envoy
        # SNI all resolve to the same per-cluster proxy-svc FQDN.
        mdbs_name = self.effective_mdbs_resource_name()
        resource = MongoDBSearch.from_yaml(
            yaml_fixture(MC_RS_SEARCH_FIXTURE),
            name=mdbs_name,
            namespace=self.namespace,
        )
        resource.api = kubernetes.client.CustomObjectsApi(get_central_cluster_client())
        if try_load(resource):
            return resource

        source = self._load_source()
        seeds = [f"{svc}.{self.namespace}.svc.cluster.local:27017" for svc in source.service_names()]
        resource["spec"]["source"] = {
            "username": self.mdb_config.mongot_user_name,
            "passwordSecretRef": {
                "name": f"{mdbs_name}-{self.mdb_config.mongot_user_name}-password",
                "key": "password",
            },
            "external": {
                "hostAndPorts": seeds,
                "tls": {"ca": {"name": self.mdb_config.ca_configmap_name}},
            },
        }
        resource["spec"]["security"] = {"tls": {"certsSecretPrefix": self.search_config.tls_cert_prefix}}
        clusters = self.search_clusters()
        for i, cluster in enumerate(clusters):
            cluster["loadBalancer"] = {
                "managed": {
                    "externalHostname": search_resource_names.mc_proxy_svc_fqdn(mdbs_name, self.namespace, i),
                },
            }
        resource["spec"]["clusters"] = clusters
        return resource

    def deploy_lb_certificates(self) -> None:
        create_mc_lb_certificates(
            namespace=self.namespace,
            issuer=ensure_search_issuer(self.namespace),
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            tls_cert_prefix=self.search_config.tls_cert_prefix,
            helper=self.deployment_helper(),
            envoy_lb_replicas=self.search_config.envoy_lb_replicas,
        )

    def create_search_tls_certificate(self) -> None:
        create_mc_mongot_tls_cert(
            namespace=self.namespace,
            issuer=ensure_search_issuer(self.namespace),
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            tls_cert_prefix=self.search_config.tls_cert_prefix,
            helper=self.deployment_helper(),
        )
        # MCK does not replicate Secrets in production; the e2e harness must.
        replicate_search_secrets_to_members(
            namespace=self.namespace,
            central_cluster_client=get_central_cluster_client(),
            member_cluster_clients=get_member_cluster_clients(),
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            mdbs_tls_cert_prefix=self.search_config.tls_cert_prefix,
            mongot_user_name=self.mdb_config.mongot_user_name,
            ca_configmap_name=self.mdb_config.ca_configmap_name,
        )

    def verify_search_deployment(self) -> None:
        member_cluster_clients = get_member_cluster_clients()
        helper = self.deployment_helper()

        verify_per_cluster_mongot_resources(
            mdb=self._load_source(),
            namespace=self.namespace,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            helper=helper,
            member_cluster_clients=member_cluster_clients,
        )
        verify_per_cluster_envoy_deployment(
            namespace=self.namespace,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            helper=helper,
            member_cluster_clients=member_cluster_clients,
        )
        mdbs = self.build_mdbs()
        mdbs.load()
        mdbs.assert_lb_status()
        verify_per_cluster_envoy_sni(
            namespace=self.namespace,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            helper=helper,
            member_cluster_clients=member_cluster_clients,
        )

    def wire_mongot_host(self) -> None:
        patch_per_cluster_mongot_host_via_om(
            mdb=self._load_source(),
            helper=self.deployment_helper(),
            member_cluster_clients=get_member_cluster_clients(),
            envoy_proxy_port=self.search_config.envoy_proxy_port,
        )

    def verify_mongod_parameters(self) -> None:
        assert_per_cluster_mongot_host_observed(
            mdb=self._load_source(),
            helper=self.deployment_helper(),
            member_cluster_clients=get_member_cluster_clients(),
            envoy_proxy_port=self.search_config.envoy_proxy_port,
        )


# ===========================================================================
# Sharded topology — MultiCluster sharded source + per-(cluster, shard) search.
# ===========================================================================


class MongoDBMultiShardedDeploymentTests(MongoDBSourceDeploymentTests):
    """External MultiCluster sharded MongoDB source.

    Per-component TLS certs → MongoDB (Running) → central-cluster users. Sibling of
    ``MongoDBMultiRsDeploymentTests``; the source is a ``MongoDB`` with
    ``topology: MultiCluster`` and per-component ``clusterSpecList`` (NOT a MongoDBMulti).
    """

    create_timeout: int = 1800

    def users_api_client(self) -> kubernetes.client.ApiClient:
        # MongoDBUser CRs for an MC source live in the central cluster.
        return get_central_cluster_client()

    def ensure_ca_configmap(self) -> str:
        # Central first (operator reads here), then every member cluster so the sharded
        # source mongod pods can verify peer TLS during reconcile (operator does not
        # replicate the CA for this path). Mirrors q3's ca_configmap fixture.
        name = super().ensure_ca_configmap()
        for mcc in get_member_cluster_clients():
            create_issuer_ca(
                get_issuer_ca_filepath(),
                self.namespace,
                self.mdb_config.ca_configmap_name,
                api_client=mcc.api_client,
            )
        return name

    def build_source(self) -> MongoDB:
        # Spec-level mongotHost is omitted on purpose: it is wired per-(cluster,shard)
        # into the AC later.
        self.ensure_ca_configmap()
        resource = MongoDB.from_yaml(
            yaml_fixture(MC_SHARDED_SOURCE_FIXTURE),
            name=self.mdb_config.mdb_resource_name,
            namespace=self.namespace,
        )
        resource.api = kubernetes.client.CustomObjectsApi(get_central_cluster_client())
        if try_load(resource):
            return resource
        resource.configure(
            om=get_ops_manager(self.namespace),
            project_name=self.mdb_config.mdb_resource_name,
            api_client=get_central_cluster_client(),
        )
        resource["spec"]["shardCount"] = self.mdb_config.shard_count
        member_names = get_member_cluster_names()
        uniform: list[int | None] = [1] * len(member_names)
        # Tag each shard member with its member cluster ("nodeLocation": <clusterName>)
        # so a nearest+tagSet read preference can pin a $search to a same-cluster member
        # per shard (proves cluster-locality of search routing). Only votes/priority
        # default to 1 when omitted, so tags-only member configs are safe.
        shard_member_configs = [
            [{"tags": {"nodeLocation": name}} for _ in range(count or 0)] for name, count in zip(member_names, uniform)
        ]
        resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(
            member_names, uniform, member_configs=shard_member_configs
        )
        resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(member_names, uniform)
        resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(member_names, uniform)

        search_set_parameter = {"setParameter": dict(SEARCH_SET_PARAMETERS)}
        resource["spec"]["shard"]["additionalMongodConfig"] = search_set_parameter
        resource["spec"]["mongos"]["additionalMongodConfig"] = search_set_parameter
        resource["spec"]["security"] = {
            "certsSecretPrefix": self.mdb_config.source_cert_prefix,
            "tls": {"ca": self.mdb_config.ca_configmap_name},
            "authentication": {"enabled": True, "modes": ["SCRAM"]},
        }
        return resource

    def install_source_tls_certificates(self) -> None:
        # ShardedCluster + certsSecretPrefix expects one secret per component, not a
        # bundle: {prefix}-{resource}-{N}-cert per shard, plus -config-cert / -mongos-cert.
        # Each cert SANs every member-cluster cross-cluster pod FQDN.
        issuer = ensure_search_issuer(self.namespace)
        central = get_central_cluster_client()
        prefix = self.mdb_config.source_cert_prefix
        mdb_name = self.mdb_config.mdb_resource_name
        uniform: list[int | None] = [1] * len(get_member_cluster_names())

        def _issue(component_resource: str, secret_name: str) -> None:
            create_tls_certs(
                issuer=issuer,
                namespace=self.namespace,
                resource_name=component_resource,
                replicas_cluster_distribution=uniform,
                secret_name=secret_name,
                api_client=central,
            )

        for shard_idx in range(self.mdb_config.shard_count):
            _issue(f"{mdb_name}-{shard_idx}", f"{prefix}-{mdb_name}-{shard_idx}-cert")
        _issue(f"{mdb_name}-config", f"{prefix}-{mdb_name}-config-cert")
        _issue(f"{mdb_name}-mongos", f"{prefix}-{mdb_name}-mongos-cert")
        logger.info(f"MC sharded source per-component TLS certs created (prefix={prefix})")


class SearchShardedMcDeploymentTests(SearchShardedDeploymentTests):
    def cluster_indexes(self) -> List[int]:
        return list(range(len(get_member_cluster_names())))

    def search_api_client(self) -> kubernetes.client.ApiClient:
        return get_central_cluster_client()

    def search_clusters(self) -> list:
        return [
            {
                "name": name,
                "index": i,
                "replicas": self.search_config.mongot_replicas,
                "resourceRequirements": self.search_config.mongot_resource_requirements(),
            }
            for i, name in enumerate(get_member_cluster_names())
        ]

    def source_router_hosts(self) -> List[str]:
        # One mongos per member cluster; per-pod mongos headless Services are reachable
        # cross-cluster via Istio.
        mdb_name = self.mdb_config.mdb_resource_name
        return [
            f"{mdb_name}-mongos-{cluster_idx}-0-svc.{self.namespace}.svc.cluster.local:27017"
            for cluster_idx in range(len(get_member_cluster_names()))
        ]

    def source_shards(self) -> list:
        # One mongod (member 0) per shard per member cluster.
        mdb_name = self.mdb_config.mdb_resource_name
        cluster_count = len(get_member_cluster_names())
        return [
            {
                "shardName": f"{mdb_name}-{shard_idx}",
                "hosts": [
                    f"{mdb_name}-{shard_idx}-{cluster_idx}-0-svc.{self.namespace}.svc.cluster.local:27017"
                    for cluster_idx in range(cluster_count)
                ],
            }
            for shard_idx in range(self.mdb_config.shard_count)
        ]

    def _load_source(self) -> MongoDB:
        resource = MongoDB(name=self.mdb_config.mdb_resource_name, namespace=self.namespace)
        resource.api = kubernetes.client.CustomObjectsApi(get_central_cluster_client())
        resource.load()
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
            api_client=get_central_cluster_client(),
        )

    def create_search_tls_certificate(self) -> None:
        issuer = ensure_search_issuer(self.namespace)
        central = get_central_cluster_client()
        for cluster_index in self.cluster_indexes():
            create_per_shard_search_tls_certs(
                self.namespace,
                issuer,
                self.search_config.tls_cert_prefix,
                self.mdb_config.shard_count,
                self.mdb_config.mdb_resource_name,
                self.effective_mdbs_resource_name(),
                cluster_index=cluster_index,
                api_client=central,
            )
        # MCK does not replicate Secrets in production; the e2e harness must.
        replicate_sharded_search_secrets_to_members(
            namespace=self.namespace,
            central_cluster_client=central,
            member_cluster_clients=get_member_cluster_clients(),
            mdb_resource_name=self.mdb_config.mdb_resource_name,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            mdbs_tls_cert_prefix=self.search_config.tls_cert_prefix,
            shard_count=self.mdb_config.shard_count,
            mongot_user_name=self.mdb_config.mongot_user_name,
        )

    def verify_search_deployment(self) -> None:
        mdbs_name = self.effective_mdbs_resource_name()
        for cluster_index, mcc in enumerate(get_member_cluster_clients()):
            assert_deployment_ready_in_cluster(
                mcc.apps_v1_api(),
                name=search_resource_names.lb_deployment_name(mdbs_name, cluster_index=cluster_index),
                namespace=self.namespace,
            )
            # Cluster-level proxy Service (mongos target) must exist on this cluster.
            mcc.read_namespaced_service(
                search_resource_names.mc_proxy_svc_name(mdbs_name, cluster_index), self.namespace
            )
            # Each (cluster, shard) pair has its own mongot StatefulSet.
            for shard_idx in range(self.mdb_config.shard_count):
                shard_name = f"{self.mdb_config.mdb_resource_name}-{shard_idx}"
                mcc.read_namespaced_stateful_set(
                    search_resource_names.shard_statefulset_name(mdbs_name, shard_name, cluster_index),
                    self.namespace,
                )
            logger.info(f"[{mcc.cluster_name}] MC sharded search deployment verified (cluster_index={cluster_index})")

    def wire_mongot_host(self) -> None:
        mdb = self._load_source()
        patch_per_cluster_sharded_mongot_host_via_om(
            mdb=mdb,
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            namespace=self.namespace,
            shard_count=self.mdb_config.shard_count,
            cluster_indexes=self.cluster_indexes(),
            envoy_proxy_port=self.search_config.envoy_proxy_port,
            multi_cluster=True,
        )
        mdb.assert_reaches_phase(Phase.Running, timeout=900)

    def verify_mongod_parameters(self) -> None:
        member_clients = get_member_cluster_clients()
        assert_sharded_mongot_host_observed(
            mdb=self._load_source(),
            mdbs_resource_name=self.effective_mdbs_resource_name(),
            namespace=self.namespace,
            shard_count=self.mdb_config.shard_count,
            cluster_indexes=self.cluster_indexes(),
            envoy_proxy_port=self.search_config.envoy_proxy_port,
            multi_cluster=True,
            member_api_client_by_cluster={i: mcc.api_client for i, mcc in enumerate(member_clients)},
        )
