import yaml
from kubetester import (
    create_or_update_secret,
    get_service,
    run_periodically,
    try_load,
)
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
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod
from tests.common.search import movies_search_helper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

MDB_RESOURCE_NAME = "mdb-ent-tls"

# MongoDBSearch TLS configuration
MDBS_TLS_SECRET_NAME = "mdbs-tls-secret"


@fixture(scope="function")
def mdb(namespace: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    resource.configure_custom_tls(issuer_ca_configmap, "certs")

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)

    if try_load(resource):
        return resource

    # Add TLS configuration to MongoDBSearch
    if "spec" not in resource:
        resource["spec"] = {}

    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}

    return resource


@fixture(scope="function")
def admin_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-admin.yaml"), namespace=namespace, name=ADMIN_USER_NAME
    )

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodbuser-mdb-user.yaml"), namespace=namespace, name=USER_NAME)

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
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

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["username"] = MONGOT_USER_NAME
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@mark.e2e_search_enterprise_tls
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_enterprise_tls
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_tls
def test_install_tls_secrets_and_configmaps(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    search_service_name = f"{mdbs.name}-search-svc"
    create_tls_certs(
        issuer,
        namespace,
        f"{mdbs.name}-search",
        replicas=1,
        service_name=search_service_name,
        additional_domains=[f"{search_service_name}.{namespace}.svc.cluster.local"],
        secret_name=MDBS_TLS_SECRET_NAME,
    )


@mark.e2e_search_enterprise_tls
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
def test_create_users(
    namespace: str, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser, mdb: MongoDB
):
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
    mongot_user.assert_reaches_phase(Phase.Updated, timeout=300)


@mark.e2e_search_enterprise_tls
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


# After picking up MongoDBSearch CR, MongoDB reconciler will add mongod parameters to each process.
# Due to how MongoDB reconciler works (blocking on waiting for agents and not changing the status to pending)
# the phase won't be updated to Pending and we need to wait by checking agents' status directly in OM.
@mark.e2e_search_enterprise_tls
def test_wait_for_agents_ready(mdb: MongoDB):
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
def test_wait_for_mongod_parameters(mdb: MongoDB):
    # After search CR is deployed, MongoDB controller will pick it up
    # and start adding search-related parameters to the automation config.
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


@mark.e2e_search_enterprise_tls
def test_search_verify_prometheus_disabled_initially(mdbs: MongoDBSearch):
    assert_search_service_prometheus_port(mdbs, should_exist=False)
    assert_search_pod_prometheus_endpoint(mdbs, should_be_accessible=False)


@mark.e2e_search_enterprise_tls
def test_search_enable_prometheus_on_default_port(mdbs: MongoDBSearch):
    mdbs["spec"]["prometheus"] = {}
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
def test_search_verify_prometheus_enabled(mdbs: MongoDBSearch):
    assert_search_service_prometheus_port(mdbs, should_exist=True, expected_port=9946)
    assert_search_pod_prometheus_endpoint(mdbs, should_be_accessible=True, port=9946)


@mark.e2e_search_enterprise_tls
def test_search_change_prometheus_to_custom_port(mdbs: MongoDBSearch):
    mdbs["spec"]["prometheus"] = {"port": 10000}
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
def test_search_verify_prometheus_enabled_on_custom_port(mdbs: MongoDBSearch):
    assert_search_service_prometheus_port(mdbs, should_exist=True, expected_port=10000)
    assert_search_pod_prometheus_endpoint(mdbs, should_be_accessible=True, port=10000)


@mark.e2e_search_enterprise_tls
def test_search_restore_sample_database(mdb: MongoDB):
    get_admin_sample_movies_helper(mdb).restore_sample_database()


@mark.e2e_search_enterprise_tls
def test_search_create_search_index(mdb: MongoDB):
    get_user_sample_movies_helper(mdb).create_search_index()


@mark.e2e_search_enterprise_tls
def test_search_assert_search_query(mdb: MongoDB):
    get_user_sample_movies_helper(mdb).assert_search_query(retry_timeout=60)


def get_connection_string(mdb: MongoDB, user_name: str, user_password: str) -> str:
    return f"mongodb://{user_name}:{user_password}@{mdb.name}-0.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017/?replicaSet={mdb.name}"


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


def assert_search_service_prometheus_port(mdbs: MongoDBSearch, should_exist: bool, expected_port: int = 9946):
    service_name = f"{mdbs.name}-search-svc"
    service = get_service(mdbs.namespace, service_name)
    assert service is not None

    ports = {p.name: p.port for p in service.spec.ports}

    if should_exist:
        assert "prometheus" in ports
        assert ports["prometheus"] == expected_port
    else:
        assert "prometheus" not in ports


def assert_search_pod_prometheus_endpoint(mdbs: MongoDBSearch, should_be_accessible: bool, port: int = 9946):
    from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod

    service_fqdn = f"{mdbs.name}-search-svc.{mdbs.namespace}.svc.cluster.local"
    url = f"http://{service_fqdn}:{port}/metrics"

    tools_pod = get_tools_pod(namespace=mdbs.namespace)
    if should_be_accessible:
        # We don't necessarily need the connectivity test to run via a bastion pod as we could connect to it directly when running test in pod.
        # But it's not requiring forwarding when running locally.
        result = tools_pod.run_command(["curl", "-f", "-s", url])
        assert "# HELP" in result or "# TYPE" in result

        logger.info(f"Prometheus endpoint is accessible at {url} and returning metrics")
    else:
        try:
            result = tools_pod.run_command(["curl", "-f", "-s", "--max-time", "5", url])
            assert False, f"Prometheus endpoint should not be accessible but got: {result}"
        except Exception as e:
            logger.info(f"Expected failure: Prometheus endpoint is not accessible at {url}: {e}")
