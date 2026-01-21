"""
E2E test for MongoDB Search with TLS and envoy proxy using two replica sets.

This test verifies the Search + envoy proxy implementation with multiple replica sets:
- Deploys two MongoDB replica sets with TLS (mdb-rs-1 and mdb-rs-2)
- Deploys MongoDBSearch with envoy proxy annotation for each replica set
- Verifies mongod parameters are set correctly for each replica set
- Runs search queries through both replica sets
"""

from __future__ import annotations

import yaml
from kubetester import create_or_update_secret, run_periodically, try_load
from kubetester.certs import create_mongodb_tls_certs, create_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import movies_search_helper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager
from tests.search.search_enterprise_tls import deploy_mongodb_tools_pod

logger = test_logger.get_test_logger(__name__)

# Number of replica sets to deploy
RS_COUNT = 2


class ReplicaSetSearchDeployment:
    """Encapsulates MongoDB replica set and MongoDBSearch deployment with TLS and envoy proxy."""

    def __init__(self, index: int, namespace: str, issuer_ca_configmap: str, issuer: str):
        self.index = index
        self.namespace = namespace
        self.issuer_ca_configmap = issuer_ca_configmap
        self.issuer = issuer

        # Generate names based on index
        self.mdb_name = f"mdb-rs-{index}"
        self.admin_user_name = f"mdb-admin-user-{index}"
        self.user_name = f"mdb-user-{index}"
        # mongot_user_name is NOT indexed - search controller expects standard "search-sync-source" name
        self.mongot_user_name = "search-sync-source"
        self.tls_secret_name = f"mdbs-tls-secret-{index}"

        # Passwords derived from names
        self.admin_user_password = f"{self.admin_user_name}-password"
        self.user_password = f"{self.user_name}-password"
        self.mongot_user_password = f"{self.mongot_user_name}-password"

    def get_mdb(self) -> MongoDB:
        """Get or create MongoDB replica set resource using indexed name."""
        resource = MongoDB.from_yaml(
            yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
            name=self.mdb_name,
            namespace=self.namespace,
        )

        if try_load(resource):
            return resource

        resource.configure(om=get_ops_manager(self.namespace), project_name=self.mdb_name)
        resource.configure_custom_tls(self.issuer_ca_configmap, "certs")
        return resource

    def get_mdbs(self) -> MongoDBSearch:
        """Get or create MongoDBSearch resource with envoy proxy using indexed name."""
        resource = MongoDBSearch.from_yaml(
            yaml_fixture("search-minimal.yaml"),
            namespace=self.namespace,
            name=self.mdb_name,
        )

        if try_load(resource):
            return resource

        if "spec" not in resource:
            resource["spec"] = {}
        resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": self.tls_secret_name}}}

        if "metadata" not in resource:
            resource["metadata"] = {}
        if "annotations" not in resource["metadata"]:
            resource["metadata"]["annotations"] = {}
        resource["metadata"]["annotations"]["use-proxy"] = "true"
        return resource

    def get_admin_user(self) -> MongoDBUser:
        """Get or create admin user resource using indexed name."""
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-admin.yaml"),
            namespace=self.namespace,
            name=self.admin_user_name,
        )

        if try_load(resource):
            return resource

        resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_name
        resource["spec"]["username"] = self.admin_user_name
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{self.admin_user_name}-password"
        return resource

    def get_user(self) -> MongoDBUser:
        """Get or create regular user resource using indexed name."""
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-user.yaml"),
            namespace=self.namespace,
            name=self.user_name,
        )

        if try_load(resource):
            return resource

        resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_name
        resource["spec"]["username"] = self.user_name
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{self.user_name}-password"
        return resource

    def get_mongot_user(self) -> MongoDBUser:
        """Get or create mongot user resource using indexed name."""
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
            namespace=self.namespace,
            name=f"{self.mdb_name}-{self.mongot_user_name}",
        )

        if try_load(resource):
            return resource

        resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_name
        resource["spec"]["username"] = self.mongot_user_name
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        return resource

    def install_tls_certs(self):
        """Create TLS certificates for replica set and search service."""
        mdb = self.get_mdb()
        mdbs = self.get_mdbs()

        # Create TLS certs for replica set
        create_mongodb_tls_certs(
            self.issuer, self.namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members()
        )

        # Create TLS certs for search service
        search_service_name = f"{mdbs.name}-search-svc"
        create_tls_certs(
            self.issuer,
            self.namespace,
            f"{mdbs.name}-search",
            replicas=1,
            service_name=search_service_name,
            additional_domains=[f"{search_service_name}.{self.namespace}.svc.cluster.local"],
            secret_name=self.tls_secret_name,
        )

    # Replica set deployment - split into update and assert phases
    def update_replica_set(self):
        """Update/create replica set resource (non-blocking)."""
        mdb = self.get_mdb()
        mdb.update()

    def assert_replica_set_running(self):
        """Assert replica set reaches Running phase."""
        mdb = self.get_mdb()
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    # Users creation - split into update and assert phases
    def update_users(self):
        """Create secrets and update all user resources (non-blocking)."""
        admin_user = self.get_admin_user()
        user = self.get_user()
        mongot_user = self.get_mongot_user()

        # Create admin user secret and update
        create_or_update_secret(
            self.namespace,
            name=admin_user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": self.admin_user_password},
        )
        admin_user.update()

        # Create regular user secret and update
        create_or_update_secret(
            self.namespace,
            name=user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": self.user_password},
        )
        user.update()

        # Create mongot user secret and update
        create_or_update_secret(
            self.namespace,
            name=mongot_user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": self.mongot_user_password},
        )
        mongot_user.update()

    def assert_users_updated(self):
        """Assert all users reach Updated phase."""
        admin_user = self.get_admin_user()
        user = self.get_user()
        mongot_user = self.get_mongot_user()

        admin_user.assert_reaches_phase(Phase.Updated, timeout=300)
        user.assert_reaches_phase(Phase.Updated, timeout=300)
        mongot_user.assert_reaches_phase(Phase.Updated, timeout=300)

    # Search deployment - split into update and assert phases
    def update_search(self):
        """Update/create MongoDBSearch resource (non-blocking)."""
        mdbs = self.get_mdbs()
        mdbs.update()

    def assert_search_running(self):
        """Assert MongoDBSearch reaches Running phase."""
        mdbs = self.get_mdbs()
        mdbs.assert_reaches_phase(Phase.Running, timeout=300)

    def wait_for_agents_ready(self):
        """Wait for MongoDB agents to be ready."""
        mdb = self.get_mdb()
        mdb.get_om_tester().wait_agents_ready()
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    def wait_for_mongod_parameters(self):
        """Wait for mongod search parameters to be set."""
        mdb = self.get_mdb()

        def check_mongod_parameters():
            parameters_are_set = True
            pod_parameters = []
            for idx in range(mdb.get_members()):
                mongod_config = yaml.safe_load(
                    KubernetesTester.run_command_in_pod_container(
                        f"{mdb.name}-{idx}", mdb.namespace, ["cat", "/data/automation-mongod.conf"]
                    )
                )
                set_parameter = mongod_config.get("setParameter", {})
                parameters_are_set = parameters_are_set and (
                    "mongotHost" in set_parameter and "searchIndexManagementHostAndPort" in set_parameter
                )
                pod_parameters.append(f"pod {idx} setParameter: {set_parameter}")

            return parameters_are_set, f'Not all pods have mongot parameters set:\n{"\n".join(pod_parameters)}'

        run_periodically(check_mongod_parameters, timeout=600)

    def restore_sample_database(self):
        """Restore sample movies database."""
        self._get_admin_sample_movies_helper().restore_sample_database()

    def create_search_index(self):
        """Create search index on sample database."""
        self._get_user_sample_movies_helper().create_search_index()

    def assert_search_query(self):
        """Execute and verify search query."""
        self._get_user_sample_movies_helper().assert_search_query(retry_timeout=60)

    def verify_search_status(self):
        """Verify MongoDBSearch is in Running phase."""
        mdbs = self.get_mdbs()
        mdbs.load()

        phase = mdbs.get_status_phase()
        assert phase == Phase.Running.value, f"MongoDBSearch phase is {phase}, expected Running"

        logger.info(f"MongoDBSearch {mdbs.name} is in Running phase")

    def _get_connection_string(self, user_name: str, user_password: str) -> str:
        """Get connection string for replica set."""
        mdb = self.get_mdb()
        return f"mongodb://{user_name}:{user_password}@{mdb.name}-0.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017/?replicaSet={mdb.name}"

    def _get_admin_sample_movies_helper(self):
        from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod

        mdb = self.get_mdb()
        return movies_search_helper.SampleMoviesSearchHelper(
            SearchTester(
                self._get_connection_string(self.admin_user_name, self.admin_user_password),
                use_ssl=True,
                ca_path=get_issuer_ca_filepath(),
            ),
            tools_pod=get_tools_pod(namespace=mdb.namespace),
        )

    def _get_user_sample_movies_helper(self):
        from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod

        mdb = self.get_mdb()
        return movies_search_helper.SampleMoviesSearchHelper(
            SearchTester(
                self._get_connection_string(self.user_name, self.user_password),
                use_ssl=True,
                ca_path=get_issuer_ca_filepath(),
            ),
            tools_pod=get_tools_pod(namespace=mdb.namespace),
        )


@fixture(scope="function")
def rs_deployments(namespace: str, issuer_ca_configmap: str, issuer: str) -> list[ReplicaSetSearchDeployment]:
    """Create deployment helpers for all replica sets."""
    return [
        ReplicaSetSearchDeployment(i + 1, namespace, issuer_ca_configmap, issuer)
        for i in range(RS_COUNT)
    ]


@mark.e2e_search_enterprise_tls_rs_envoy
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_enterprise_tls_rs_envoy
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_tls_rs_envoy
def test_install_tls_secrets_and_configmaps(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Create TLS certificates for all replica sets and their search services."""
    for deployment in rs_deployments:
        deployment.install_tls_certs()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_create_replica_sets(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Deploy all replica sets in parallel."""
    # Update all replica sets first (parallel deployment)
    for deployment in rs_deployments:
        deployment.update_replica_set()

    # Then assert all reach Running phase
    for deployment in rs_deployments:
        deployment.assert_replica_set_running()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_create_users(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Create users for all replica sets in parallel."""
    # Update all users first (parallel deployment)
    for deployment in rs_deployments:
        deployment.update_users()

    # Then assert all reach Updated phase
    for deployment in rs_deployments:
        deployment.assert_users_updated()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_create_search_resources(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Deploy MongoDBSearch resources for all replica sets in parallel."""
    # Update all search resources first (parallel deployment)
    for deployment in rs_deployments:
        deployment.update_search()

    # Then assert all reach Running phase
    for deployment in rs_deployments:
        deployment.assert_search_running()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_wait_for_agents_ready(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Wait for agents to be ready for all replica sets."""
    for deployment in rs_deployments:
        deployment.wait_for_agents_ready()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_wait_for_mongod_parameters(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Wait for mongod parameters to be set for all replica sets."""
    for deployment in rs_deployments:
        deployment.wait_for_mongod_parameters()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_search_deploy_tools_pod(namespace: str):
    """Deploy mongodb-tools pod for connectivity testing."""
    deploy_mongodb_tools_pod(namespace)


@mark.e2e_search_enterprise_tls_rs_envoy
def test_search_restore_sample_databases(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Restore sample movies database for all replica sets."""
    for deployment in rs_deployments:
        deployment.restore_sample_database()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_search_create_search_indexes(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Create search indexes on sample databases for all replica sets."""
    for deployment in rs_deployments:
        deployment.create_search_index()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_search_assert_search_queries(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Execute and verify search queries on all replica sets."""
    for deployment in rs_deployments:
        deployment.assert_search_query()


@mark.e2e_search_enterprise_tls_rs_envoy
def test_verify_search_resource_status(rs_deployments: list[ReplicaSetSearchDeployment]):
    """Verify all MongoDBSearch resources are in Running phase."""
    for deployment in rs_deployments:
        deployment.verify_search_status()
