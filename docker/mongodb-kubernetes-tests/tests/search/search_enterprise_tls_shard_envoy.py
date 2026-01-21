"""
E2E test for sharded MongoDB Search with TLS and envoy proxy.

This test verifies the sharded Search + envoy proxy implementation:
- Deploys a sharded MongoDB cluster with TLS
- Deploys MongoDBSearch with per-shard external LB endpoints and envoy proxy annotation
- Verifies per-shard mongot Services are created
- Verifies per-shard mongot StatefulSets are created
- Verifies mongod parameters are set correctly for each shard
- Runs search queries through the sharded cluster
"""

import yaml
from kubetester import create_or_update_secret, get_service, get_statefulset, try_load
from kubetester.certs import create_sharded_cluster_certs, create_tls_certs
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

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = f"{MDB_1_RESOURCE_NAME}-search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = f"{MDB_1_RESOURCE_NAME}-user"
USER_PASSWORD = f"{USER_NAME}-password"

# MongoDBSearch TLS configuration
MDBS_TLS_SECRET_NAME = "mdbs-tls-secret"


@fixture(scope="function")
def mdb(namespace: str, issuer_ca_configmap: str) -> MongoDB:
    """Fixture for sharded MongoDB cluster with TLS."""
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_1_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    # Configure OpsManager/CloudManager connection
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_1_RESOURCE_NAME)

    # Configure TLS for sharded cluster
    resource["spec"]["security"]["tls"] = {"ca": issuer_ca_configmap}

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    """Fixture for MongoDBSearch with sharded external LB configuration and envoy proxy."""
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-sharded-external-lb.yaml"),
        namespace=namespace,
        name=MDB_1_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    # Update the endpoints with the actual namespace and resource name
    spec = resource["spec"]
    if "lb" in spec and "external" in spec["lb"] and "sharded" in spec["lb"]["external"]:
        # Rebuild endpoints for this resource name and namespace
        endpoints = []
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_1_RESOURCE_NAME}-{shard_idx}"
            endpoint = f"{MDB_1_RESOURCE_NAME}-mongot-{shard_idx}-proxy-svc.{namespace}.svc.cluster.local:27029"
            endpoints.append({"shardName": shard_name, "endpoint": endpoint})
        spec["lb"]["external"]["sharded"]["endpoints"] = endpoints

    # Add TLS configuration to MongoDBSearch
    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}

    # Add envoy proxy annotation
    resource["metadata"]["annotations"] = {
        "use-proxy": "true"
    }

    return resource


@fixture(scope="function")
def admin_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-admin.yaml"), namespace=namespace, name=ADMIN_USER_NAME
    )

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_1_RESOURCE_NAME
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodbuser-mdb-user.yaml"), namespace=namespace, name=USER_NAME)

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_1_RESOURCE_NAME
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def mongot_user(namespace: str, mdbs: MongoDBSearch) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
        namespace=namespace,
        name=f"{mdbs.name}-{MONGOT_USER_NAME}",
    )

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_1_RESOURCE_NAME
    resource["spec"]["username"] = MONGOT_USER_NAME
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@mark.e2e_search_enterprise_tls_envoy
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_enterprise_tls_envoy
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_tls_envoy
def test_install_tls_secrets_and_configmaps(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch, issuer: str):
    """Create TLS certificates for sharded cluster and search services."""
    # Create TLS certs for sharded cluster (shards, config servers, mongos)
    create_sharded_cluster_certs(
        namespace,
        mdb.name,
        shards=SHARD_COUNT,
        mongod_per_shard=MONGODS_PER_SHARD,
        config_servers=CONFIG_SERVER_COUNT,
        mongos=MONGOS_COUNT,
    )

    # Create TLS certs for each shard's mongot service
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{mdb.name}-{shard_idx}"
        search_service_name = f"{mdbs.name}-mongot-{shard_name}-svc"
        secret_name = f"{MDBS_TLS_SECRET_NAME}-{shard_idx}"
        create_tls_certs(
            issuer,
            namespace,
            f"{mdbs.name}-mongot-{shard_name}",
            replicas=1,
            service_name=search_service_name,
            additional_domains=[f"{search_service_name}.{namespace}.svc.cluster.local"],
            secret_name=secret_name,
        )

    # Also create a general search TLS cert for the main secret
    create_tls_certs(
        issuer,
        namespace,
        f"{mdbs.name}-search",
        replicas=1,
        service_name=f"{mdbs.name}-search-svc",
        additional_domains=[f"{mdbs.name}-search-svc.{namespace}.svc.cluster.local"],
        secret_name=MDBS_TLS_SECRET_NAME,
    )


@mark.e2e_search_enterprise_tls_envoy
def test_create_sharded_cluster(mdb: MongoDB):
    """Test sharded cluster deployment."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_enterprise_tls_envoy
def test_create_users(
    namespace: str, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser, mdb: MongoDB
):
    """Test user creation for the sharded cluster."""
    create_or_update_secret(
        namespace, name=admin_user["spec"]["passwordSecretKeyRef"]["name"], data={"password": ADMIN_USER_PASSWORD}
    )
    admin_user.update()
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace, name=user["spec"]["passwordSecretKeyRef"]["name"], data={"password": USER_PASSWORD}
    )
    user.update()
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace, name=mongot_user["spec"]["passwordSecretKeyRef"]["name"], data={"password": MONGOT_USER_PASSWORD}
    )
    mongot_user.update()
    # Don't wait for mongot user - it needs searchCoordinator role from Search CR


@mark.e2e_search_enterprise_tls_envoy
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with sharded external LB and envoy proxy."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_tls_envoy
def test_verify_per_shard_services(namespace: str, mdbs: MongoDBSearch):
    """
    Verify that per-shard mongot Services are created.

    For a sharded cluster with external LB, the Search controller should create
    one Service per shard with naming: <search-name>-mongot-<shardName>-svc
    """
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_1_RESOURCE_NAME}-{shard_idx}"
        service_name = f"{mdbs.name}-mongot-{shard_name}-svc"

        logger.info(f"Checking for per-shard Service: {service_name}")
        service = get_service(namespace, service_name)

        assert service is not None, f"Per-shard Service {service_name} not found"
        assert service.spec is not None, f"Service {service_name} has no spec"

        # Verify the service has the expected port
        ports = {p.name: p.port for p in service.spec.ports}
        assert "mongot" in ports or 27028 in ports.values(), \
            f"Service {service_name} does not have mongot port (27028)"

        logger.info(f"Per-shard Service {service_name} exists with ports: {ports}")


@mark.e2e_search_enterprise_tls_envoy
def test_verify_per_shard_statefulsets(namespace: str, mdbs: MongoDBSearch):
    """
    Verify that per-shard mongot StatefulSets are created.

    For a sharded cluster with external LB, the Search controller should create
    one StatefulSet per shard with naming: <search-name>-mongot-<shardName>
    """
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_1_RESOURCE_NAME}-{shard_idx}"
        sts_name = f"{mdbs.name}-mongot-{shard_name}"

        logger.info(f"Checking for per-shard StatefulSet: {sts_name}")

        try:
            sts = get_statefulset(namespace, sts_name)
            assert sts is not None, f"Per-shard StatefulSet {sts_name} not found"
            assert sts.status is not None, f"StatefulSet {sts_name} has no status"

            # Verify the StatefulSet has at least 1 ready replica
            ready_replicas = sts.status.ready_replicas or 0
            assert ready_replicas >= 1, \
                f"StatefulSet {sts_name} has {ready_replicas} ready replicas, expected >= 1"

            logger.info(f"Per-shard StatefulSet {sts_name} exists with {ready_replicas} ready replicas")
        except Exception as e:
            raise AssertionError(f"Failed to get per-shard StatefulSet {sts_name}: {e}")


@mark.e2e_search_enterprise_tls_envoy
def test_verify_mongod_parameters_per_shard(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    """
    Verify that each shard's mongod has the correct search parameters.

    For sharded clusters with external LB, each shard should have:
    - mongotHost pointing to its shard-specific external LB endpoint
    - searchIndexManagementHostAndPort pointing to the same endpoint
    """
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_1_RESOURCE_NAME}-{shard_idx}"
        # Pod name for single mongod per shard: <shardName>-0
        pod_name = f"{shard_name}-0"

        logger.info(f"Checking mongod parameters for shard {shard_name} (pod: {pod_name})")

        mongod_config = yaml.safe_load(
            KubernetesTester.run_command_in_pod_container(
                pod_name, namespace, ["cat", "/data/automation-mongod.conf"]
            )
        )

        set_parameter = mongod_config.get("setParameter", {})

        # Verify mongotHost is set
        assert "mongotHost" in set_parameter, \
            f"mongotHost not found in setParameter for shard {shard_name}"

        # Verify searchIndexManagementHostAndPort is set
        assert "searchIndexManagementHostAndPort" in set_parameter, \
            f"searchIndexManagementHostAndPort not found in setParameter for shard {shard_name}"

        mongot_host = set_parameter["mongotHost"]
        search_mgmt_host = set_parameter["searchIndexManagementHostAndPort"]

        # For external LB mode, the endpoint should contain the shard-specific service name
        expected_shard_service = f"{mdbs.name}-mongot-{shard_name}-svc"

        logger.info(f"  mongotHost: {mongot_host}")
        logger.info(f"  searchIndexManagementHostAndPort: {search_mgmt_host}")

        assert expected_shard_service in mongot_host, \
            f"mongotHost for shard {shard_name} should contain {expected_shard_service}, got: {mongot_host}"

        assert expected_shard_service in search_mgmt_host, \
            f"searchIndexManagementHostAndPort for shard {shard_name} should contain {expected_shard_service}, got: {search_mgmt_host}"

        logger.info(f"Shard {shard_name} has correct search parameters")


# After picking up MongoDBSearch CR, MongoDB reconciler will add mongod parameters to each process.
# Due to how MongoDB reconciler works (blocking on waiting for agents and not changing the status to pending)
# the phase won't be updated to Pending and we need to wait by checking agents' status directly in OM.
@mark.e2e_search_enterprise_tls_envoy
def test_wait_for_agents_ready(mdb: MongoDB):
    """Wait for agents to be ready after search parameters are applied."""
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls_envoy
def test_search_deploy_tools_pod(namespace: str):
    """Deploy mongodb-tools pod for connectivity testing."""
    deploy_mongodb_tools_pod(namespace)


@mark.e2e_search_enterprise_tls_envoy
def test_search_restore_sample_database(mdb: MongoDB):
    """Restore sample movies database for search testing."""
    get_admin_sample_movies_helper(mdb).restore_sample_database()


@mark.e2e_search_enterprise_tls_envoy
def test_search_create_search_index(mdb: MongoDB):
    """Create search index on sample movies database."""
    get_user_sample_movies_helper(mdb).create_search_index()


@mark.e2e_search_enterprise_tls_envoy
def test_search_assert_search_query(mdb: MongoDB):
    """Execute and verify search query on the sharded cluster."""
    get_user_sample_movies_helper(mdb).assert_search_query(retry_timeout=120)


def get_connection_string(mdb: MongoDB, user_name: str, user_password: str) -> str:
    """
    Get connection string for sharded cluster via mongos.

    For sharded clusters, we connect through mongos instead of directly to replica set members.
    """
    # For sharded cluster, connect via mongos service
    mongos_host = f"{mdb.name}-mongos-0.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017"
    return f"mongodb://{user_name}:{user_password}@{mongos_host}/"


def get_admin_sample_movies_helper(mdb):
    from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod

    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD),
            use_ssl=True,
            ca_path=get_issuer_ca_filepath(),
        ),
        tools_pod=get_tools_pod(namespace=mdb.namespace),
    )


def get_user_sample_movies_helper(mdb):
    from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod

    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdb, USER_NAME, USER_PASSWORD), use_ssl=True, ca_path=get_issuer_ca_filepath()
        ),
        tools_pod=get_tools_pod(namespace=mdb.namespace),
    )


@mark.e2e_search_enterprise_tls_envoy
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase with correct status."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running.value, f"MongoDBSearch phase is {phase}, expected Running"

    logger.info(f"MongoDBSearch {mdbs.name} is in Running phase")
