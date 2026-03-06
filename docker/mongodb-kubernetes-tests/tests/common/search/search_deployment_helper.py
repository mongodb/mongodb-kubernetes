from typing import Callable, Optional

from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_sharded_cluster_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)


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
        lb_mode: str = "Unmanaged",
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

        if lb_endpoint:
            resource["spec"]["lb"] = {"mode": lb_mode, "endpoint": lb_endpoint}

        return resource

    def admin_user_resource(self, admin_user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-admin.yaml"),
            namespace=self.namespace,
            name=admin_user_name,
        )
        if try_load(resource):
            return resource
        resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
        resource["spec"]["username"] = resource.name
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        return resource

    def user_resource(self, user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-user.yaml"),
            namespace=self.namespace,
            name=user_name,
        )
        if try_load(resource):
            return resource
        resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
        resource["spec"]["username"] = resource.name
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        return resource

    def mongot_user_resource(self, mdbs: MongoDBSearch, mongot_user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
            namespace=self.namespace,
            name=f"{mdbs.name}-{mongot_user_name}",
        )
        if try_load(resource):
            return resource
        resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
        resource["spec"]["username"] = mongot_user_name
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        return resource

    def deploy_users(
        self,
        admin_user: MongoDBUser,
        admin_password: str,
        user: MongoDBUser,
        user_password: str,
        mongot_user: MongoDBUser,
        mongot_password: str,
    ):
        create_or_update_secret(
            self.namespace,
            name=admin_user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": admin_password},
        )
        admin_user.update()
        admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

        create_or_update_secret(
            self.namespace,
            name=user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": user_password},
        )
        user.update()
        user.assert_reaches_phase(Phase.Updated, timeout=300)

        create_or_update_secret(
            self.namespace,
            name=mongot_user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": mongot_password},
        )
        mongot_user.update()
        # Don't wait for mongot user — needs searchCoordinator role from Search CR

    def install_sharded_tls_certificates(self, secret_prefix: str = "mdb-sh-"):
        mongos_service_dns = f"{self.mdb_resource_name}-svc.{self.namespace}.svc.cluster.local"
        create_sharded_cluster_certs(
            namespace=self.namespace,
            resource_name=self.mdb_resource_name,
            shards=self.shard_count,
            mongod_per_shard=self.mongods_per_shard,
            config_servers=self.config_server_count,
            mongos=self.mongos_count,
            secret_prefix=secret_prefix,
            mongos_service_dns_names=[mongos_service_dns],
        )
        logger.info("Sharded cluster TLS certificates created")
