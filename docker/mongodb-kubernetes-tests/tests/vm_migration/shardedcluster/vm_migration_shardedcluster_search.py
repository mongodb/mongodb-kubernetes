"""
VM migration of a sharded cluster that has a MongoDBSearch resource attached.

Search is first attached to the VM sharded cluster through an external sharded source, proving it
works before migration. The search resource then keeps that external source for the whole
migration: its shard seeds and router hosts are updated by hand as members move from the VMs to
Kubernetes, and search must survive promote and prune.

The VM shard replica set is deliberately named vm-shard-0, which differs from the Kubernetes shard
name <MDB_RESOURCE_NAME>-0. migrate-to-mck therefore emits shardNameOverrides, and the per-shard
seed lookup has to translate the Kubernetes shard name to the Automation Config replica set name.
Renaming the VM shard to match the Kubernetes name would make this test pass even if that
translation were wrong.
"""

from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongotester import with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.scram import build_sha256_creds
from pytest import fixture, mark
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.vm_migration.vm_migration_common_helper import (
    assert_migration_data_exists,
    generated_mongodb_doc,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_sharded_helper import (
    MIN_VM_CONFIGSRV,
    MIN_VM_MONGOS,
    MIN_VM_SHARD,
    apply_generated_sharded_cluster_resource,
    assert_connection_string_after_full_sharded_migration,
    assert_k8s_sharded_process_names,
    build_sharded_cluster_ac,
    deploy_vm_sharded_configsrv_service,
    deploy_vm_sharded_configsrv_statefulset,
    deploy_vm_sharded_mongos_service,
    deploy_vm_sharded_mongos_statefulset,
    deploy_vm_sharded_shard_service,
    deploy_vm_sharded_shard_statefulset,
    promote_and_prune_shard,
    vm_mongos_tester,
)

MDB_VERSION = "8.2.0-ent"

CONFIGSRV_STS_NAME = "vm-sharded-configsrv"
SHARD_STS_NAME = "vm-sharded-shard"
MONGOS_STS_NAME = "vm-sharded-mongos"
CONFIGSRV_SVC_NAME = "vm-sharded-configsrv"
SHARD_SVC_NAME = "vm-sharded-shard"
MONGOS_SVC_NAME = "vm-sharded-mongos"

VM_CONFIG_RS_NAME = "vm-config"
VM_SHARD_RS_NAME = "vm-shard-0"
VM_MONGOS_NAME = "vm-mongos"

# Pinned so the mongot endpoints hand-written into the automation config below can be computed
# before the CR exists: they are derived from these two names, neither of which changes during the
# migration, so the endpoints stay resolvable throughout.
MDB_RESOURCE_NAME = "sharded-search-migration"
MDBS_RESOURCE_NAME = "sharded-search"
K8S_SHARD_NAME = f"{MDB_RESOURCE_NAME}-0"
MONGOT_PORT = 27028

AGENT_PASSWORD = "mms-automation-agent-password"
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

KEYFILE_SECRET_NAME = "vm-sharded-keyfile"
KEYFILE_CONTENTS = "dm0tbWlncmF0aW9uLXNoYXJkZWQtc2VhcmNoLWtleWZpbGUtcGFk"
MONGOT_PASSWORD_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password"

# The mongot setParameters the operator would write itself. mongotHost and
# searchIndexManagementHostAndPort are added per-test once the endpoint is known. Kept in sync with
# buildSearchSetParameters (controllers/searchcontroller/mongodbsearch_reconcile_helper.go); the same
# keys are carried into the generated CR: they are no longer listed in infrastructureFieldPaths
# in controllers/om/process.go, because Ops Manager does not propagate them onto new processes.
SEARCH_SET_PARAMETER = {
    "skipAuthenticationToSearchIndexManagementServer": False,
    "skipAuthenticationToMongot": False,
    "searchTLSMode": "disabled",
    "useGrpcForSearch": True,
}


def _vm_shard_hosts(namespace: str) -> list[str]:
    return [f"{SHARD_STS_NAME}-{i}.{SHARD_SVC_NAME}.{namespace}.svc.cluster.local:27017" for i in range(MIN_VM_SHARD)]


def _vm_mongos_hosts(namespace: str) -> list[str]:
    return [
        f"{MONGOS_STS_NAME}-{i}.{MONGOS_SVC_NAME}.{namespace}.svc.cluster.local:27017" for i in range(MIN_VM_MONGOS)
    ]


def _k8s_shard_hosts(mdb_migration: MongoDB) -> list[str]:
    """Pod FQDNs of the Kubernetes shard mongods, in the format the operator uses for the shard
    StatefulSet's headless Service (<mdb>-sh)."""
    return [
        f"{K8S_SHARD_NAME}-{i}.{mdb_migration.name}-sh.{mdb_migration.namespace}.svc.cluster.local:27017"
        for i in range(mdb_migration["spec"]["mongodsPerShardCount"])
    ]


def _k8s_mongos_hosts(mdb_migration: MongoDB) -> list[str]:
    return [
        f"{mdb_migration.name}-mongos-{i}.{mdb_migration.name}-svc.{mdb_migration.namespace}.svc.cluster.local:27017"
        for i in range(mdb_migration["spec"]["mongosCount"])
    ]


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_configsrv_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_configsrv_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_shard_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_shard_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_mongos_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_mongos_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_configsrv_service(namespace: str):
    return deploy_vm_sharded_configsrv_service(namespace)


@fixture(scope="module")
def vm_shard_service(namespace: str):
    return deploy_vm_sharded_shard_service(namespace)


@fixture(scope="module")
def vm_mongos_service(namespace: str):
    return deploy_vm_sharded_mongos_service(namespace)


@fixture(scope="module")
def mongot_host(namespace: str) -> str:
    """The per-shard mongot endpoint, keyed by the KUBERNETES shard name.

    This is the value written into the VM automation config below, and nothing rewrites it later:
    the search resource keeps its external source, so the endpoint has to stay resolvable for the
    whole migration. It does, because the mongot StatefulSet is named from the MongoDBSearch
    resource and the shard entry's shardName, neither of which changes.
    """
    return search_resource_names.shard_pod_fqdn(MDBS_RESOURCE_NAME, K8S_SHARD_NAME, namespace, MONGOT_PORT)


def _configure_ac_with_search(namespace: str, om_tester: OMTester, mongot_host: str):
    """SCRAM sharded cluster with the mongot sync-source user and the mongot setParameters.

    The setParameters go on the shard mongods and on the mongos, which is where the operator puts
    them for a sharded cluster. The config server never gets them: mongot neither syncs from nor
    routes through it.
    """
    existing = om_tester.api_get_automation_config()
    if len(existing.get("processes", [])) > 0:
        return

    ac = build_sharded_cluster_ac(
        om_tester,
        configsrv_sts_name=CONFIGSRV_STS_NAME,
        shard_sts_name=SHARD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        configsrv_service_name=CONFIGSRV_SVC_NAME,
        shard_service_name=SHARD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=MDB_VERSION,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=MIN_VM_CONFIGSRV,
        shard_count=MIN_VM_SHARD,
        mongos_count=MIN_VM_MONGOS,
        cluster_name=VM_MONGOS_NAME,
    )

    search_params = dict(SEARCH_SET_PARAMETER)
    search_params["mongotHost"] = mongot_host
    search_params["searchIndexManagementHostAndPort"] = mongot_host
    for process in ac["processes"]:
        is_shard_mongod = process["args2_6"].get("replication", {}).get("replSetName") == VM_SHARD_RS_NAME
        if is_shard_mongod or process["processType"] == "mongos":
            process["args2_6"]["setParameter"] = dict(search_params)

    ac["auth"] = {
        "usersWanted": [
            {
                "user": "mms-automation-agent",
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(AGENT_PASSWORD),
                "authenticationRestrictions": [],
            },
            {
                "user": ADMIN_USER_NAME,
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(ADMIN_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
            {
                # Mirrors the roles in the mongodbuser-search-sync-source-user.yaml fixture, which
                # cannot be used here because there is no MongoDB CR yet.
                "user": MONGOT_USER_NAME,
                "db": "admin",
                "roles": [{"role": "searchCoordinator", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(MONGOT_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
        "disabled": False,
        "authoritativeSet": False,
        "deploymentAuthMechanisms": ["SCRAM-SHA-256"],
        "autoAuthMechanisms": ["SCRAM-SHA-256"],
        "autoAuthMechanism": "SCRAM-SHA-256",
        "autoUser": "mms-automation-agent",
        "autoPwd": AGENT_PASSWORD,
        "autoAuthRestrictions": [],
        "key": KEYFILE_CONTENTS,
        "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
        "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
    }

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def mdbs(namespace: str, vm_shard_sts: dict, vm_mongos_sts: dict) -> MongoDBSearch:
    """MongoDBSearch pointing at the VM shard and VM mongos through an external sharded source.

    shardName is the KUBERNETES shard name the generated CR will use, not the VM replica set name:
    the mongot StatefulSet is named from it, and that name has to stay put for the whole migration,
    otherwise the mongotHost baked into the automation config stops resolving.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-sharded-external-mongod.yaml"), namespace=namespace, name=MDBS_RESOURCE_NAME
    )
    if try_load(resource):
        return resource

    # The fixture ships security.tls.certsSecretPrefix for the TLS search suites; this deployment is
    # plaintext, so the whole security block goes and external.tls is left out.
    resource["spec"].pop("security", None)

    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {"name": MONGOT_PASSWORD_SECRET_NAME, "key": "password"},
        "external": {
            "shardedCluster": {
                "router": {"hosts": _vm_mongos_hosts(namespace)},
                "shards": [{"shardName": K8S_SHARD_NAME, "hosts": _vm_shard_hosts(namespace)}],
            },
            # Field name is `keyfileSecretRef` on the wire (api/mongodb/v1/search/
            # mongodbsearch_types.go), despite the Go struct field being named KeyFileSecretKeyRef.
            "keyfileSecretRef": {"name": KEYFILE_SECRET_NAME, "key": "keyfile"},
        },
    }
    return resource


@fixture(scope="module")
def scram_opts() -> list[dict]:
    return [with_scram(ADMIN_USER_NAME, ADMIN_USER_PASSWORD, "SCRAM-SHA-256")]


@fixture(scope="module")
def sample_movies_helper(namespace: str) -> SampleMoviesSearchHelper:
    # Built by hand rather than via SearchTester.for_sharded because no MongoDB CR exists yet, and
    # vm_mongos_tester's URI carries no credentials. Mongos routers are not a replica set, so no
    # replicaSet= option here.
    hosts = ",".join(_vm_mongos_hosts(namespace))
    cnx_string = f"mongodb://{ADMIN_USER_NAME}:{ADMIN_USER_PASSWORD}@{hosts}/?authSource=admin"
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(cnx_string), tools_pod=mongodb_tools_pod.get_tools_pod(namespace)
    )


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    admin_secret = f"{ADMIN_USER_NAME}-migration-password"
    create_or_update_secret(namespace, name=admin_secret, data={"password": ADMIN_USER_PASSWORD})
    mongot_secret = f"{MONGOT_USER_NAME}-migration-password"
    create_or_update_secret(namespace, name=mongot_secret, data={"password": MONGOT_USER_PASSWORD})
    # The SCRAM users in the hand-written automation config have to be mapped to Secrets here:
    # without --users-secrets-file the users subcommand prompts for each Secret name on stdin
    # (collectUserSecretNamesInteractively), which has nowhere to read from under pytest.
    #
    # Only the MongoDB document is applied (apply_generated_sharded_cluster_resource picks it out);
    # the generated MongoDBUser CRs are never applied. That's deliberate: the automation config sets
    # authoritativeSet: false, which the operator carries over as ignoreUnknownUsers: true, so it
    # won't prune the VM-created users just because no matching MongoDBUser CRs exist.
    return run_generate_cr(
        namespace,
        resource_name_override=MDB_RESOURCE_NAME,
        user_secrets={
            f"{ADMIN_USER_NAME}:admin": admin_secret,
            f"{MONGOT_USER_NAME}:admin": mongot_secret,
        },
    )


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    return apply_generated_sharded_cluster_resource(namespace, generated_cr_yaml, VM_CONFIG_RS_NAME)


# VM phase


@mark.e2e_vm_migration_shardedcluster_search
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_vm_migration_shardedcluster_search
def test_deploy_vm_sharded_cluster(
    namespace: str,
    om_tester: OMTester,
    vm_configsrv_sts,
    vm_shard_sts,
    vm_mongos_sts,
    vm_configsrv_service,
    vm_shard_service,
    vm_mongos_service,
    mongot_host: str,
):
    _configure_ac_with_search(namespace, om_tester, mongot_host)
    om_tester.wait_agents_ready()


@mark.e2e_vm_migration_shardedcluster_search
def test_create_search_secrets(namespace: str):
    create_or_update_secret(namespace, name=MONGOT_PASSWORD_SECRET_NAME, data={"password": MONGOT_USER_PASSWORD})
    # Manual equivalent of mirrorKeyfileIntoSecretForMongot, which only runs for MongoDB CRs.
    create_or_update_secret(namespace, name=KEYFILE_SECRET_NAME, data={"keyfile": KEYFILE_CONTENTS})


@mark.e2e_vm_migration_shardedcluster_search
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_shardedcluster_search
@skip_if_local
def test_insert_migration_data(namespace: str, scram_opts: list[dict]):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_search
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_vm_migration_shardedcluster_search
def test_search_create_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_vm_migration_shardedcluster_search
def test_search_query_works_before_migration(sample_movies_helper: SampleMoviesSearchHelper):
    """Establishes the baseline: without this, the post-migration assertion proves nothing."""
    sample_movies_helper.assert_search_query(retry_timeout=120)


# Migration phase


@mark.e2e_vm_migration_shardedcluster_search
def test_generated_cr_has_shard_name_override(mdb_migration: MongoDB):
    """The VM shard replica set name differs from the Kubernetes shard name, so migrate-to-mck must
    emit a shardNameOverride. The per-shard seed lookup depends on translating between the two."""
    try_load(mdb_migration)
    overrides = {o["shardName"]: o.get("replicaSetName") for o in mdb_migration["spec"].get("shardNameOverrides", [])}
    assert overrides.get(K8S_SHARD_NAME) == VM_SHARD_RS_NAME, f"unexpected shardNameOverrides: {overrides}"


@mark.e2e_vm_migration_shardedcluster_search
def test_generated_cr_carries_search_set_parameters(generated_cr_yaml: str, mongot_host: str):
    """The generated CR must carry the search setParameters for the shard mongods and mongos, and
    none for the config servers.

    This source has a single shard, so all shard mongods share one mongot endpoint and the config
    lands in spec.shard rather than spec.shardOverrides. A multi-shard source has a different
    endpoint per shard and produces one shardOverrides entry per shard instead; that shape is
    covered by the search_per_shard_mongot Go fixture.

    Asserted against the generated document rather than the applied CR: the customer step in
    apply_generated_sharded_cluster_resource adds a per-shard shardOverride of its own for the
    votes, so the applied CR always has shardOverrides regardless of the source topology.
    """
    spec = generated_mongodb_doc(generated_cr_yaml)["spec"]
    expected = dict(SEARCH_SET_PARAMETER, mongotHost=mongot_host, searchIndexManagementHostAndPort=mongot_host)

    for component in ("shard", "mongos"):
        set_parameter = spec[component]["additionalMongodConfig"]["setParameter"]
        for key, value in expected.items():
            assert (
                set_parameter.get(key) == value
            ), f"spec.{component} setParameter.{key} is {set_parameter.get(key)!r}, expected {value!r}"

    assert "additionalMongodConfig" not in spec.get(
        "configSrv", {}
    ), "config servers are not wired to mongot, so they must carry no additionalMongodConfig"

    assert "shardOverrides" not in spec, "a single-shard source is homogeneous"


@mark.e2e_vm_migration_shardedcluster_search
def test_dry_run_passes(mdb_migration: MongoDB):
    """run_migration_dry_run_connectivity_passes removes the dry-run annotation itself, so the next
    reconcile is already a real one."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_search
def test_migration_proceeds_while_search_still_targets_vms(mdb_migration: MongoDB):
    """The migration is not held back by the MongoDBSearch still pointing at the VM mongods."""
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_search
def test_add_kubernetes_host_seeds_to_search_source(namespace: str, mdbs: MongoDBSearch, mdb_migration: MongoDB):
    """The search resource keeps its external sharded source for the whole migration; only the seed
    lists are maintained by hand.

    Both the shard seeds and the router hosts are widened to the union of the VM and Kubernetes
    addresses rather than swapped outright: mongot has to keep at least one reachable seed and one
    reachable router at every point of the promote/prune sequence that follows.

    The shard entry's shardName stays K8S_SHARD_NAME throughout -- it names the mongot StatefulSet,
    and changing it would move the mongot endpoint the automation config already points at.
    """
    mdbs.load()
    sharded = mdbs["spec"]["source"]["external"]["shardedCluster"]
    sharded["router"]["hosts"] = _vm_mongos_hosts(namespace) + _k8s_mongos_hosts(mdb_migration)
    sharded["shards"] = [
        {"shardName": K8S_SHARD_NAME, "hosts": _vm_shard_hosts(namespace) + _k8s_shard_hosts(mdb_migration)}
    ]
    mdbs.update()

    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_shardedcluster_search
def test_mongot_config_contains_both_vm_and_kubernetes_hosts(
    namespace: str, mdbs: MongoDBSearch, mdb_migration: MongoDB
):
    """The direct assertion on the hand-maintained seed lists.

    Both address sets have to reach mongot's per-shard ConfigMap -- the shard seeds from
    source.external.shardedCluster.shards[].hosts and the router hosts from ...router.hosts.
    Asserting the ConfigMap contents rather than "search still works" pins the CR -> mongot-config
    path regardless of which members happen to be serving at the time.
    """
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)

    cm_name = search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, K8S_SHARD_NAME)
    config = "".join(KubernetesTester.read_configmap(namespace, cm_name).values())

    for host in _vm_shard_hosts(namespace) + _k8s_shard_hosts(mdb_migration):
        assert host in config, f"shard seed {host} missing from mongot config {cm_name}:\n{config}"
    for host in _vm_mongos_hosts(namespace) + _k8s_mongos_hosts(mdb_migration):
        assert host in config, f"router host {host} missing from mongot router config {cm_name}:\n{config}"


@mark.e2e_vm_migration_shardedcluster_search
def test_search_query_works_mid_migration(sample_movies_helper: SampleMoviesSearchHelper):
    """Queries still go through the VM mongos while both VM and Kubernetes members are in the
    cluster, so search must keep working with a mixed membership."""
    sample_movies_helper.assert_search_query(retry_timeout=180)


# Promote and prune


@mark.e2e_vm_migration_shardedcluster_search
def test_promote_and_prune_config_server(mdb_migration: MongoDB, om_tester: OMTester):
    """Promote one Kubernetes config member to voting, then prune one VM config member, repeat.

    Kept as a per-member loop rather than a bulk clear of externalMembers: the migration validator
    permits only one kind of change per update, and the Kubernetes config members start non-voting,
    so pruning voting VM config members first would strand the config replica set.
    """
    try_load(mdb_migration)
    for i in range(MIN_VM_CONFIGSRV):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        config_external = [
            m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == VM_CONFIG_RS_NAME
        ]
        if config_external:
            mdb_migration["spec"]["externalMembers"].remove(config_external[-1])
            mdb_migration.update()
            mdb_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_search
def test_promote_and_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    promote_and_prune_shard(mdb_migration, om_tester, VM_SHARD_RS_NAME, VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_search
def test_prune_mongos(mdb_migration: MongoDB):
    """Mongos routers carry no votes, so all of them can go in one update -- still a single change
    kind (removing external members), which the migration validator allows."""
    try_load(mdb_migration)
    for m in [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_search
def test_drop_vm_host_seeds_from_search_source(mdbs: MongoDBSearch, mdb_migration: MongoDB):
    """The VM members are out of the cluster now, so their hostnames come out of the seed lists.
    This is the second and last hand-edit of the search resource in the whole flow."""
    mdbs.load()
    sharded = mdbs["spec"]["source"]["external"]["shardedCluster"]
    sharded["router"]["hosts"] = _k8s_mongos_hosts(mdb_migration)
    sharded["shards"] = [{"shardName": K8S_SHARD_NAME, "hosts": _k8s_shard_hosts(mdb_migration)}]
    mdbs.update()

    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_shardedcluster_search
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_search
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_search
def test_search_set_parameters_in_automation_config(om_tester: OMTester, namespace: str, mongot_host: str):
    """The Kubernetes shard mongods and mongos must carry the same search setParameters the VM
    processes had, and the config servers none.

    The generated CR is what puts them there (see test_generated_cr_carries_search_set_parameters):
    no MongoDBSearch targets this MongoDB resource, so the sharded reconciler's search overrides
    never run, and Ops Manager does not propagate the deployment's existing search configuration
    onto new processes. Because the values are carried verbatim from the VM processes, the mongos
    keep the mongot pod FQDN the VM mongos were given rather than the per-shard proxy Service the
    operator would compute for an internal source.
    """
    mongos_host = mongot_host

    ac = om_tester.api_get_automation_config()
    k8s_processes = [p for p in ac["processes"] if p["name"].startswith(f"k8s/{namespace}/")]

    shard_mongods, mongos_procs, configsrv_procs = [], [], []
    for p in k8s_processes:
        if p["processType"] == "mongos":
            mongos_procs.append(p)
        elif p["args2_6"].get("replication", {}).get("replSetName") == VM_CONFIG_RS_NAME:
            configsrv_procs.append(p)
        else:
            shard_mongods.append(p)

    # Guards against the assertions below passing vacuously on an empty list.
    assert shard_mongods and mongos_procs and configsrv_procs, f"unexpected process split: {k8s_processes}"

    for processes, expected_host in ((shard_mongods, mongot_host), (mongos_procs, mongos_host)):
        expected = dict(SEARCH_SET_PARAMETER, mongotHost=expected_host, searchIndexManagementHostAndPort=expected_host)
        for process in processes:
            set_parameter = process["args2_6"].get("setParameter", {})
            for key, value in expected.items():
                assert (
                    set_parameter.get(key) == value
                ), f"process {process['name']}: setParameter.{key} is {set_parameter.get(key)!r}, expected {value!r}"

    for process in configsrv_procs:
        set_parameter = process["args2_6"].get("setParameter", {})
        assert "mongotHost" not in set_parameter, f"config server {process['name']} should not be wired to mongot"


@mark.e2e_vm_migration_shardedcluster_search
def test_search_resource_still_running(mdbs: MongoDBSearch):
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_shardedcluster_search
def test_search_query_works_after_migration(mdb_migration: MongoDB, namespace: str):
    tester = SearchTester.for_sharded(mdb_migration, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    helper = movies_search_helper.SampleMoviesSearchHelper(tester, tools_pod=mongodb_tools_pod.get_tools_pod(namespace))
    helper.assert_search_query(retry_timeout=180)


@mark.e2e_vm_migration_shardedcluster_search
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)
