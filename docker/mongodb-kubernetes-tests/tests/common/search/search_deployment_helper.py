from typing import Callable, Mapping, Optional

import kubernetes
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_mongodb_tls_certs, create_sharded_cluster_certs
from kubetester.kubetester import ensure_nested_objects
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

_ISSUER_BY_NAMESPACE: dict[str, str] = {}


def ensure_search_issuer(namespace: str) -> str:
    """Install cert-manager (idempotent) and create the namespace ``ca-issuer``.

    Explicit replacement for the ``issuer`` / ``multi_cluster_issuer`` fixtures —
    both just create the same per-namespace CA Issuer (named ``ca-issuer``) once
    cert-manager is up. Memoized per namespace so repeated hook calls don't re-run
    the helm install + readiness wait. Returns the Issuer name.
    """
    if namespace not in _ISSUER_BY_NAMESPACE:
        from tests.conftest import (
            create_issuer,
            get_central_cluster_client,
            get_central_cluster_name,
            install_cert_manager,
            wait_for_cert_manager_ready,
        )

        central_client = get_central_cluster_client()
        install_cert_manager(cluster_client=central_client, cluster_name=get_central_cluster_name())
        wait_for_cert_manager_ready(cluster_client=central_client)
        _ISSUER_BY_NAMESPACE[namespace] = create_issuer(namespace=namespace, api_client=central_client)
    return _ISSUER_BY_NAMESPACE[namespace]


class SearchDeploymentHelper:

    def __init__(
        self,
        namespace: str,
        mdb_resource_name: str,
        mdbs_resource_name: str,
        shard_count: int = 2,
        mongods_per_shard: int = 1,
        mongos_count: int = 1,
        config_server_count: int = 1,
        tls_cert_prefix: str = "certs",
        ca_configmap_name: Optional[str] = None,
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ):
        self.namespace = namespace
        self.mdb_resource_name = mdb_resource_name
        self.mdbs_resource_name = mdbs_resource_name
        self.shard_count = shard_count
        self.mongods_per_shard = mongods_per_shard
        self.mongos_count = mongos_count
        self.config_server_count = config_server_count
        self.tls_cert_prefix = tls_cert_prefix
        self.ca_configmap_name = ca_configmap_name or f"{mdb_resource_name}-ca"
        # Cluster the MongoDBUser CRs + their password secrets land in. SC leaves
        # this None (operator-cluster default); MC passes the central-cluster client.
        self.api_client = api_client

    # create_sharded_mdb returns the MongoDB sharded deployment resource, after setting the mongotHost
    # and shardOverrides based on the the function `mongot_host_fn`. For unmanaged loadbalancers it should return the proxy svc's endpoint.
    def create_sharded_mdb(
        self,
        mongot_host_fn: Optional[Callable[[str], str]] = None,
        set_tls_ca: bool = False,
    ) -> MongoDB:
        resource = MongoDB.from_yaml(
            yaml_fixture("enterprise-sharded-cluster-sample-mflix.yaml"),
            name=self.mdb_resource_name,
            namespace=self.namespace,
        )

        if try_load(resource):
            return resource

        resource.configure(om=get_ops_manager(self.namespace), project_name=self.mdb_resource_name)

        if set_tls_ca:
            resource["spec"]["security"]["tls"]["ca"] = self.ca_configmap_name

        if mongot_host_fn is not None:
            shard_overrides = []
            for shard_idx in range(self.shard_count):
                shard_name = f"{self.mdb_resource_name}-{shard_idx}"
                host = mongot_host_fn(shard_name)
                shard_overrides.append(
                    {
                        "shardNames": [shard_name],
                        "additionalMongodConfig": {
                            "setParameter": {
                                "mongotHost": host,
                                "searchIndexManagementHostAndPort": host,
                                "skipAuthenticationToSearchIndexManagementServer": False,
                                "skipAuthenticationToMongot": False,
                                "searchTLSMode": "requireTLS",
                                "useGrpcForSearch": True,
                            }
                        },
                    }
                )
            resource["spec"]["shardOverrides"] = shard_overrides

            first_shard = f"{self.mdb_resource_name}-0"
            mongos_host = mongot_host_fn(first_shard)
            if "mongos" not in resource["spec"]:
                resource["spec"]["mongos"] = {}
            resource["spec"]["mongos"]["additionalMongodConfig"] = {
                "setParameter": {
                    "mongotHost": mongos_host,
                    "searchIndexManagementHostAndPort": mongos_host,
                    "skipAuthenticationToSearchIndexManagementServer": False,
                    "skipAuthenticationToMongot": False,
                    "searchTLSMode": "requireTLS",
                    "useGrpcForSearch": True,
                }
            }

        return resource

    def mdbs_for_ext_sharded_source(
        self,
        mongot_user_name: str,
        lb_endpoint: Optional[str] = None,
        lb_mode: Optional[str] = None,
        replicas: Optional[int] = None,
        shard_overrides: Optional[list] = None,
    ) -> MongoDBSearch:
        resource = MongoDBSearch.from_yaml(
            yaml_fixture("search-sharded-external-mongod.yaml"),
            namespace=self.namespace,
            name=self.mdbs_resource_name,
        )

        if try_load(resource):
            return resource

        router_hosts = [
            f"{self.mdb_resource_name}-mongos-{i}.{self.mdb_resource_name}-svc.{self.namespace}.svc.cluster.local:27017"
            for i in range(self.mongos_count)
        ]

        shards = []
        for shard_idx in range(self.shard_count):
            shard_name = f"{self.mdb_resource_name}-{shard_idx}"
            shard_hosts = [
                f"{shard_name}-{m}.{self.mdb_resource_name}-sh.{self.namespace}.svc.cluster.local:27017"
                for m in range(self.mongods_per_shard)
            ]
            shards.append({"shardName": shard_name, "hosts": shard_hosts})

        resource["spec"]["source"] = {
            "username": mongot_user_name,
            "passwordSecretRef": {
                "name": f"{self.mdbs_resource_name}-{mongot_user_name}-password",
                "key": "password",
            },
            "external": {
                "shardedCluster": {
                    "router": {"hosts": router_hosts},
                    "shards": shards,
                },
                "tls": {"ca": {"name": self.ca_configmap_name}},
            },
        }

        if replicas is not None:
            resource["spec"]["clusters"] = [{"replicas": replicas}]

        if shard_overrides is not None:
            clusters = resource["spec"].get("clusters") or [{}]
            clusters[0]["shardOverrides"] = shard_overrides
            resource["spec"]["clusters"] = clusters

        if lb_mode or lb_endpoint:
            clusters = resource["spec"].get("clusters") or [{}]
            for i, cluster in enumerate(clusters):
                lb = {}
                if lb_mode == "Managed":
                    lb["managed"] = {
                        "externalHostname": search_resource_names.shard_proxy_svc_hostname_template(
                            self.mdbs_resource_name, self.namespace, i
                        ),
                        # Shard-agnostic cluster-level endpoint for mongos: the per-cluster proxy-svc
                        # FQDN (matches the LB cert SAN). Required for external sharded + managed LB.
                        "routerHostname": search_resource_names.mc_proxy_svc_fqdn(
                            self.mdbs_resource_name, self.namespace, i
                        ),
                    }
                if lb_endpoint:
                    lb["unmanaged"] = {"endpoint": lb_endpoint}
                elif lb_mode == "Unmanaged":
                    lb["unmanaged"] = {}
                cluster["loadBalancer"] = lb
            resource["spec"]["clusters"] = clusters

        return resource

    def _wire_user_api(self, resource: MongoDBUser) -> None:
        """Point the user resource at this helper's cluster when one was set.

        SC leaves ``api_client`` None (operator-cluster default); MC constructs
        the helper with the central-cluster client so the MongoDBUser lands there.
        """
        if self.api_client is not None:
            resource.api = kubernetes.client.CustomObjectsApi(self.api_client)

    def admin_user_resource(self, admin_user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-admin.yaml"),
            namespace=self.namespace,
            name=admin_user_name,
        )
        if not try_load(resource):
            resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
            resource["spec"]["username"] = resource.name
            resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        self._wire_user_api(resource)
        return resource

    def user_resource(self, user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-user.yaml"),
            namespace=self.namespace,
            name=user_name,
        )
        if not try_load(resource):
            resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
            resource["spec"]["username"] = resource.name
            resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        self._wire_user_api(resource)
        return resource

    def mongot_user_resource(self, mdbs_name: str, mongot_user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
            namespace=self.namespace,
            name=f"{mdbs_name}-{mongot_user_name}",
        )
        if not try_load(resource):
            resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
            resource["spec"]["username"] = mongot_user_name
            resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        self._wire_user_api(resource)
        return resource

    def configure_metrics_forwarder_opsmanager(self, mdbs: MongoDBSearch, mdb: MongoDB) -> None:
        """Point the metrics forwarder at the source MongoDB's Ops Manager project.

        External sources don't expose a mongodbResourceRef, so the forwarder can't auto-resolve the
        Ops Manager project the way it does for internal sources. It must instead be given the project
        ConfigMap and the agent-credentials Secret that the operator created for the source MongoDB.
        The agent-credentials Secret is named "{projectId}-group-secret" (see agents.ApiKeySecretName).

        Must be called after the source MongoDB has reached Running so that status.projectId is set.
        """
        mdb.load()
        project_id = mdb["status"]["projectId"]
        project_config_map_name = mdb["spec"]["opsManager"]["configMapRef"]["name"]
        ensure_nested_objects(mdbs, ["spec", "observability", "metricsForwarder"])
        mdbs["spec"]["observability"]["metricsForwarder"]["opsManager"] = {
            "projectConfigMapRef": {"name": project_config_map_name},
            "agentCredentials": {"name": f"{project_id}-group-secret"},
        }

    def apply_user_password(self, user_resource: MongoDBUser, password: str) -> None:
        """Create/update the user's password secret and re-apply the CR.

        The secret lands in this helper's cluster (``self.api_client``). Does not
        wait for any phase — callers assert phases.
        """
        create_or_update_secret(
            self.namespace,
            name=user_resource["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": password},
            api_client=self.api_client,
        )
        user_resource.update()

    def deploy_users(
        self,
        admin_user: MongoDBUser,
        admin_password: str,
        user: MongoDBUser,
        user_password: str,
        mongot_user: MongoDBUser,
        mongot_password: str,
    ):
        self.apply_user_password(admin_user, admin_password)
        admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

        self.apply_user_password(user, user_password)
        user.assert_reaches_phase(Phase.Updated, timeout=300)

        self.apply_user_password(mongot_user, mongot_password)
        # Don't wait for mongot user — needs searchCoordinator role from Search CR

    def create_replicaset_mdb(
        self,
        mongot_host: Optional[str] = None,
        set_tls_ca: bool = False,
        issuer_ca_configmap: Optional[str] = None,
        tls_cert_prefix: Optional[str] = None,
    ) -> MongoDB:
        resource = MongoDB.from_yaml(
            yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
            name=self.mdb_resource_name,
            namespace=self.namespace,
        )

        if try_load(resource):
            return resource

        resource.configure(om=get_ops_manager(self.namespace), project_name=self.mdb_resource_name)

        if issuer_ca_configmap and tls_cert_prefix:
            resource.configure_custom_tls(issuer_ca_configmap, tls_cert_prefix)

        if set_tls_ca:
            resource["spec"]["security"]["tls"]["ca"] = self.ca_configmap_name

        if mongot_host is not None:
            if "additionalMongodConfig" not in resource["spec"]:
                resource["spec"]["additionalMongodConfig"] = {}
            resource["spec"]["additionalMongodConfig"]["setParameter"] = {
                "mongotHost": mongot_host,
                "searchIndexManagementHostAndPort": mongot_host,
                "skipAuthenticationToSearchIndexManagementServer": False,
                "skipAuthenticationToMongot": False,
                "searchTLSMode": "requireTLS",
                "useGrpcForSearch": True,
            }

        return resource

    def install_sharded_tls_certificates(self, secret_prefix: str = "mdb-sh-", shard_count: Optional[int] = None):
        mongos_service_dns = f"{self.mdb_resource_name}-svc.{self.namespace}.svc.cluster.local"
        create_sharded_cluster_certs(
            namespace=self.namespace,
            resource_name=self.mdb_resource_name,
            shards=shard_count if shard_count is not None else self.shard_count,
            mongod_per_shard=self.mongods_per_shard,
            config_servers=self.config_server_count,
            mongos=self.mongos_count,
            secret_prefix=secret_prefix,
            mongos_service_dns_names=[mongos_service_dns],
        )
        logger.info("Sharded cluster TLS certificates created")

    def create_rs_mdb(
        self,
        set_tls: bool = False,
        mongot_host: Optional[str] = None,
    ) -> MongoDB:
        """Create an Enterprise ReplicaSet MongoDB resource."""
        resource = MongoDB.from_yaml(
            yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
            name=self.mdb_resource_name,
            namespace=self.namespace,
        )

        if try_load(resource):
            return resource

        resource.configure(om=get_ops_manager(self.namespace), project_name=self.mdb_resource_name)

        if set_tls:
            resource.configure_custom_tls(self.ca_configmap_name, "certs")

        if mongot_host is not None:
            resource["spec"]["additionalMongodConfig"] = {
                "setParameter": {
                    "mongotHost": mongot_host,
                    "searchIndexManagementHostAndPort": mongot_host,
                    "skipAuthenticationToSearchIndexManagementServer": False,
                    "skipAuthenticationToMongot": False,
                    "searchTLSMode": "requireTLS",
                    "useGrpcForSearch": True,
                }
            }

        return resource

    def install_rs_tls_certificates(self, issuer: str, members: int = 3):
        """Create MongoDB RS TLS certificates."""
        create_mongodb_tls_certs(
            issuer,
            self.namespace,
            self.mdb_resource_name,
            f"certs-{self.mdb_resource_name}-cert",
            members,
        )
        logger.info("RS TLS certificates created")

    def mdbs_for_ext_rs_source(
        self,
        mongot_user_name: str,
        members: int = 3,
        lb_mode: Optional[str] = None,
        replicas: Optional[int] = None,
        clusters: Optional[list] = None,
    ) -> MongoDBSearch:
        """Create MongoDBSearch with an external RS source.

        ``clusters`` (mutually exclusive with ``replicas``) writes spec.clusters;
        a single entry with no name models a single-cluster RS at index 0.
        """
        resource = MongoDBSearch.from_yaml(
            yaml_fixture("search-minimal.yaml"),
            namespace=self.namespace,
            name=self.mdbs_resource_name,
        )

        if try_load(resource):
            return resource

        seeds = [
            f"{self.mdb_resource_name}-{i}.{self.mdb_resource_name}-svc.{self.namespace}.svc.cluster.local:27017"
            for i in range(members)
        ]

        resource["spec"]["source"] = {
            "username": mongot_user_name,
            "passwordSecretRef": {
                "name": f"{self.mdbs_resource_name}-{mongot_user_name}-password",
                "key": "password",
            },
            "external": {
                "hostAndPorts": seeds,
                "tls": {"ca": {"name": self.ca_configmap_name}},
            },
        }

        resource["spec"]["security"] = {
            "tls": {"certsSecretPrefix": self.tls_cert_prefix},
        }

        if clusters is not None:
            resource["spec"]["clusters"] = clusters
        elif replicas is not None:
            resource["spec"]["clusters"] = [{"replicas": replicas}]

        if lb_mode:
            clusters_spec = resource["spec"].get("clusters") or [{}]
            for i, cluster in enumerate(clusters_spec):
                if lb_mode == "Managed":
                    cluster["loadBalancer"] = {
                        "managed": {
                            "externalHostname": search_resource_names.mc_proxy_svc_fqdn(
                                self.mdbs_resource_name, self.namespace, i
                            )
                        }
                    }
                elif lb_mode == "Unmanaged":
                    cluster["loadBalancer"] = {"unmanaged": {}}
            resource["spec"]["clusters"] = clusters_spec

        return resource


class MCSearchDeploymentHelper:
    """Per-cluster index and proxy-svc FQDN lookups for MC search e2e tests.

    Cluster ordering follows dict-insertion order of `member_cluster_clients`,
    which mirrors the explicit clusterIndex pins the e2e builders set on
    spec.clusters[] (0..N-1 in the same client order).
    """

    def __init__(
        self,
        namespace: str,
        mdbs_resource_name: str,
        member_cluster_clients: Mapping[str, CoreV1Api],
    ) -> None:
        self.namespace = namespace
        self.mdbs_resource_name = mdbs_resource_name
        self._member_cluster_clients = dict(member_cluster_clients)
        self._cluster_indices = {name: idx for idx, name in enumerate(self._member_cluster_clients)}

    def member_cluster_names(self) -> list[str]:
        return list(self._member_cluster_clients.keys())

    def cluster_index(self, cluster_name: str) -> int:
        if cluster_name not in self._cluster_indices:
            raise KeyError(f"unknown member cluster: {cluster_name!r}")
        return self._cluster_indices[cluster_name]

    def member_clients(self) -> Mapping[str, CoreV1Api]:
        return self._member_cluster_clients

    def proxy_svc_fqdn(self, cluster_name: str) -> str:
        return search_resource_names.mc_proxy_svc_fqdn(
            self.mdbs_resource_name, self.namespace, self.cluster_index(cluster_name)
        )
