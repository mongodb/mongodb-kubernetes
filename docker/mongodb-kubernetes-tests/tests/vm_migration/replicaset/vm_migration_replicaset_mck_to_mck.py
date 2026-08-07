"""
MCK-to-MCK VM migration.

Deploys a real MCK-managed no-auth replica set in the source namespace, stops the
source operator, then migrates the deployment into a second namespace using
kubectl-mongodb migrate-to-mck and the VM-migration promote & prune flow. Unlike the
other vm_migration replica set tests, the migration source is a genuine MCK deployment
(MCK-style process names/hostnames in the automation config) rather than the raw agent
StatefulSet fixture, and the migration runs operator -> operator across namespaces.
"""

from kubetester import create_or_update_configmap, create_or_update_secret, read_configmap, read_secret
from kubetester.kubetester import KubernetesTester, create_testing_namespace, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_central_cluster_client, get_evergreen_task_id, get_operator_installation_config
from tests.vm_migration.vm_migration_common_helper import (
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_replicaset_helper import (
    apply_generated_mongodb_resource,
    assert_common_generated_cr_shape,
    assert_connection_string_after_full_migration,
    assert_connection_string_contains_current_hosts,
    assert_k8s_process_names,
    promote_and_prune,
)

SOURCE_MEMBERS = 3
SOURCE_RS_NAME = "source-rs"
# Distinct from the source operator's default name so the two operators' cluster-scoped
# Helm resources do not collide (see target_operator fixture).
TARGET_OPERATOR_NAME = "mongodb-kubernetes-operator-target"


def shared_project_name(namespace: str) -> str:
    """Ops Manager project name shared by the source and target namespaces.

    Derived from the (per-run unique) namespace: Cloud QA shares one OM organization
    across all e2e runs, and project (group) names must be unique within an org, so a
    fixed literal would 409 with GROUP_ALREADY_EXISTS across retries and parallel runs.
    """
    return f"{namespace}-mck-to-mck"


# --- target-namespace bootstrap (inlined per design) ---


def _ensure_shared_project_name(namespace: str, api_client=None):
    """Pin an explicit projectName on the namespace's my-project ConfigMap.

    Guarantees the source deployment registers into a per-run-unique, shared project so
    the copy in the target namespace resolves the identical Ops Manager project.
    """
    project_name = shared_project_name(namespace)
    project_cm = read_configmap(namespace, "my-project", api_client=api_client)
    if project_cm.get("projectName") != project_name:
        project_cm["projectName"] = project_name
        create_or_update_configmap(namespace, "my-project", project_cm, api_client=api_client)


def _prepare_target_namespace(source_ns: str, target_ns: str, api_client):
    """Create target_ns and copy everything the target operator + import tool need.

    Copies operator install config, image-pull secret, and the shared my-project /
    my-credentials so the imported CR points at the same OM project as the source.

    Database-role ServiceAccounts are intentionally NOT pre-created here: the operator
    Helm chart installed into this namespace creates and owns them, and a pre-created
    (non-Helm-owned) copy makes the operator's helm install fail on ownership metadata.
    """
    operator_installation_config = get_operator_installation_config(source_ns)
    create_testing_namespace(get_evergreen_task_id(), target_ns, api_client=api_client)

    image_pull_secret_name = operator_installation_config["registry.imagePullSecrets"]
    image_pull_secret_data = read_secret(source_ns, image_pull_secret_name, api_client=api_client)
    create_or_update_secret(
        target_ns,
        image_pull_secret_name,
        image_pull_secret_data,
        type="kubernetes.io/dockerconfigjson",
        api_client=api_client,
    )

    op_config_cm = read_configmap(source_ns, "operator-installation-config", api_client=api_client)
    create_or_update_configmap(target_ns, "operator-installation-config", op_config_cm, api_client=api_client)

    project_cm = read_configmap(source_ns, "my-project", api_client=api_client)
    create_or_update_configmap(target_ns, "my-project", project_cm, api_client=api_client)

    credentials = read_secret(source_ns, "my-credentials", api_client=api_client)
    create_or_update_secret(target_ns, "my-credentials", credentials, api_client=api_client)


def _target_om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


# --- fixtures ---


@fixture(scope="module")
def target_namespace(namespace: str) -> str:
    return f"{namespace}-target"


@fixture(scope="module")
def source_mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    _ensure_shared_project_name(namespace)
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-single.yaml"),
        name=SOURCE_RS_NAME,
        namespace=namespace,
    )
    resource["spec"]["members"] = SOURCE_MEMBERS
    resource.set_version(ensure_ent_version(custom_mdb_version))
    return resource


@fixture(scope="module")
def target_operator(target_namespace: str) -> Operator:
    # Distinct operator.name (and Helm release name) so the target operator's
    # cluster-scoped resources -- notably the "{operator.name}-cluster-telemetry"
    # ClusterRole, whose name carries no namespace -- do not collide with the still-present
    # source operator's Helm-owned cluster resources. The runtime webhook config
    # (mdbpolicy.mongodb.com) is a shared singleton with failurePolicy=Ignore that the
    # running operator takes over, so a distinct name here is safe.
    helm_args = get_operator_installation_config(target_namespace).copy()
    helm_args["operator.watchNamespace"] = target_namespace
    helm_args["operator.name"] = TARGET_OPERATOR_NAME
    return Operator(namespace=target_namespace, helm_args=helm_args, name=TARGET_OPERATOR_NAME).install()


@fixture(scope="module")
def generated_cr_yaml(target_namespace: str) -> str:
    return run_generate_cr(target_namespace)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(target_namespace: str, generated_cr: dict) -> MongoDB:
    return apply_generated_mongodb_resource(target_namespace, generated_cr, customer_sets_disabled_tls_mode=True)


@fixture(scope="module")
def source_stub() -> dict:
    """Stand-in expected by promote_and_prune, which reads spec.replicas as the source count."""
    return {"spec": {"replicas": SOURCE_MEMBERS}}


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(mdb_migration.tester(use_ssl=False))


# --- test flow (ordered; runs under -x) ---


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_install_source_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_deploy_source_mdb(source_mdb: MongoDB):
    source_mdb.update()
    source_mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_vm_migration_replicaset_mck_to_mck
@skip_if_local()
def test_connectivity_before_migration(source_mdb: MongoDB):
    source_mdb.tester(use_ssl=False).assert_connectivity()


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_insert_migration_data(source_mdb: MongoDB):
    insert_migration_data(source_mdb.tester(use_ssl=False))


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_stop_source_operator(operator: Operator):
    """Stop operator A. Source pods and agents keep running; the OM automation config is frozen."""
    operator.delete_operator_deployment()


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_prepare_target_namespace(namespace: str, target_namespace: str):
    _prepare_target_namespace(namespace, target_namespace, get_central_cluster_client())


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_install_target_operator(target_operator: Operator):
    """Installed after operator A is gone, so operator B owns the cluster webhook."""
    target_operator.wait_for_operator_ready()


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_generate_cr_shape(generated_cr_yaml: str, generated_cr: dict, version_id: str):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, version_id, SOURCE_MEMBERS)


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_external_members_point_at_source(generated_cr: dict, namespace: str):
    external_members = generated_cr["spec"]["externalMembers"]
    assert len(external_members) == SOURCE_MEMBERS
    for member in external_members:
        assert (
            f".{namespace}.svc.cluster.local" in member["hostname"]
        ), f"external member should live in source namespace {namespace}: {member['hostname']}"


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_no_security_in_cr(generated_cr: dict):
    assert "security" not in generated_cr.get("spec", {})


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_no_user_crs_emitted(generated_cr_yaml: str):
    assert len(generated_user_docs(generated_cr_yaml)) == 0


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_migrate_to_target(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_mck_to_mck
@skip_if_local()
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_promote_and_prune(mdb_migration: MongoDB, source_stub: dict):
    promote_and_prune(mdb_migration, source_stub)


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_process_names(mdb_migration: MongoDB, target_namespace: str):
    om_tester = _target_om_tester(target_namespace)
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_mck_to_mck
@skip_if_local()
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_replicaset_mck_to_mck
@skip_if_local()
def test_connectivity_after_promote(mdb_migration: MongoDB):
    mdb_migration.tester(use_ssl=False).assert_connectivity()


@mark.e2e_vm_migration_replicaset_mck_to_mck
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False))
