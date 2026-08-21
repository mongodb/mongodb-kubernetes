"""
VM migration to a Kubernetes sharded cluster where every tier is addressed through an external domain.

Unlike the client-facing externalAccess case, a sharded migration needs config servers and shards
externally addressable too: during the migration VM members and Kubernetes members are members of the
same replica set (the config server RS, and each shard's RS) and must reach each other by the hostname
in the automation config. Single-cluster only exposed mongos before the per-tier
spec.<tier>.externalAccess fields, so this scenario was not workable at all.

MetalLB assigns LoadBalancer IPs from 172.18.255.200 upwards on kind (see
scripts/dev/recreate_kind_clusters.sh). Single-cluster external services are created inside
create.DatabaseInKubernetes, so allocation follows the tier creation order in createKubernetesResources:
config servers, then shards, then mongos. The IPs are predicted on that basis and seeded into CoreDNS
before the resource is applied, which removes any DNS race as the LoadBalancers appear. The order is
load-bearing, which is what test_external_services_created exists to catch.
"""

from kubetester import get_service, get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import default_external_domain, update_coredns_hosts
from tests.vm_migration.vm_migration_common_helper import (
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_sharded_helper import (
    MIN_K8S_CONFIGSRV,
    MIN_K8S_MONGOS,
    MIN_K8S_SHARD,
    MIN_VM_CONFIGSRV,
    MIN_VM_MONGOS,
    MIN_VM_SHARD,
    apply_generated_sharded_cluster_resource,
    assert_common_generated_sharded_cr_shape,
    assert_connection_string_after_full_sharded_migration,
    assert_k8s_sharded_process_names,
    assert_sharded_connection_string_uses_external_domain,
    build_sharded_cluster_ac,
    deploy_vm_sharded_configsrv_service,
    deploy_vm_sharded_configsrv_statefulset,
    deploy_vm_sharded_mongos_service,
    deploy_vm_sharded_mongos_statefulset,
    deploy_vm_sharded_shard_service,
    deploy_vm_sharded_shard_statefulset,
    external_mongos_tester,
    k8s_tier_hostnames,
    promote_and_prune_shard,
    vm_mongos_tester,
)

CONFIGSRV_STS_NAME = "vm-sharded-configsrv"
SHARD_STS_NAME = "vm-sharded-shard"
MONGOS_STS_NAME = "vm-sharded-mongos"
CONFIGSRV_SVC_NAME = "vm-sharded-configsrv"
SHARD_SVC_NAME = "vm-sharded-shard"
MONGOS_SVC_NAME = "vm-sharded-mongos"
MDB_RESOURCE_NAME = "sharded-migration"
VM_CONFIG_RS_NAME = "vm-config"
VM_SHARD_RS_NAME = "vm-shard-0"
VM_MONGOS_NAME = "vm-mongos"

EXTERNAL_DOMAIN = default_external_domain()
LB_IP_BASE = "172.18.255.200"

# Single-cluster external services are created inside create.DatabaseInKubernetes, so MetalLB hands
# out addresses in the tier creation order of createKubernetesResources: config, shards, mongos.
TIER_ALLOCATION_ORDER = (
    (f"{MDB_RESOURCE_NAME}-config", MIN_K8S_CONFIGSRV),
    (f"{MDB_RESOURCE_NAME}-0", MIN_K8S_SHARD),
    (f"{MDB_RESOURCE_NAME}-mongos", MIN_K8S_MONGOS),
)


def _external_service_names_in_allocation_order() -> list[str]:
    return [f"{prefix}-{i}-svc-external" for prefix, count in TIER_ALLOCATION_ORDER for i in range(count)]


def _external_fqdns_in_allocation_order() -> list[str]:
    return [f"{prefix}-{i}.{EXTERNAL_DOMAIN}" for prefix, count in TIER_ALLOCATION_ORDER for i in range(count)]


def _predicted_lb_ips() -> list[str]:
    first, last_octet = LB_IP_BASE.rsplit(".", 1)
    total = sum(count for _, count in TIER_ALLOCATION_ORDER)
    return [f"{first}.{int(last_octet) + i}" for i in range(total)]


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sharded_configsrv_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_configsrv_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_shard_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_shard_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_mongos_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_mongos_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_configsrv_service(namespace: str):
    return deploy_vm_sharded_configsrv_service(namespace)


@fixture(scope="module")
def vm_sharded_shard_service(namespace: str):
    return deploy_vm_sharded_shard_service(namespace)


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    return deploy_vm_sharded_mongos_service(namespace)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_generate_cr(namespace, resource_name_override=MDB_RESOURCE_NAME)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    def add_per_tier_external_access(resource_doc: dict):
        # migrate-to-mck does not emit externalAccess, so the customer adds it when expanding into
        # Kubernetes. See test_external_access_not_generated. Per-tier rather than top-level: the
        # top-level field only reaches mongos in single-cluster, which would leave the config server
        # and shard replica sets advertising internal hostnames the VM members cannot resolve.
        external_access = {
            "externalDomain": EXTERNAL_DOMAIN,
            "externalService": {
                "spec": {
                    "type": "LoadBalancer",
                    "ports": [{"name": "mongodb", "port": 27017}],
                }
            },
        }
        for component in ("configSrv", "shard", "mongos"):
            resource_doc["spec"].setdefault(component, {})["externalAccess"] = dict(external_access)

    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
        customer_sets_disabled_tls_mode=True,
        prepare_external_resources=add_per_tier_external_access,
    )


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(external_mongos_tester(mdb_migration))


# Test flow


@mark.e2e_vm_migration_shardedcluster_external_access
def test_update_coredns():
    """Seed CoreDNS before the resource exists, so external FQDNs resolve as soon as the LBs appear."""
    update_coredns_hosts(list(zip(_predicted_lb_ips(), _external_fqdns_in_allocation_order())))


@mark.e2e_vm_migration_shardedcluster_external_access
def test_deploy_vm_sharded(
    namespace: str,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
):
    for sts_body in (vm_sharded_configsrv_sts, vm_sharded_shard_sts, vm_sharded_mongos_sts):

        def sts_is_ready(body=sts_body):
            sts = get_statefulset(namespace, body["metadata"]["name"])
            return sts.status.ready_replicas == body["spec"]["replicas"]

        KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
    custom_mdb_version: str,
):
    mdb_version = ensure_ent_version(custom_mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac.get("processes", [])) > 0:
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
        mongodb_version=mdb_version,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=MIN_VM_CONFIGSRV,
        shard_count=MIN_VM_SHARD,
        mongos_count=MIN_VM_MONGOS,
        cluster_name=VM_MONGOS_NAME,
        compressors="snappy,zstd",
        directory_per_db=True,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_external_access
@skip_if_local()
def test_connectivity_before_migration(namespace: str):
    """Sharded cluster is reachable without authentication before migration."""
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_external_access
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_vm_migration_shardedcluster_external_access
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace))


# Generated CR checks


@mark.e2e_vm_migration_shardedcluster_external_access
def test_common_generated_cr_shape(generated_cr: dict, version_id: str):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=MIN_VM_CONFIGSRV,
        expected_shard_count=MIN_VM_SHARD,
        expected_mongos_count=MIN_VM_MONGOS,
        version_id=version_id,
    )


@mark.e2e_vm_migration_shardedcluster_external_access
def test_generated_resource_name_matches_expectation(generated_cr: dict):
    """CoreDNS is seeded from MDB_RESOURCE_NAME before the CR exists, so the name must match."""
    actual = generated_cr["metadata"]["name"]
    assert actual == MDB_RESOURCE_NAME, (
        f"expected generated resource name {MDB_RESOURCE_NAME!r}, got {actual!r}. "
        f"CoreDNS was seeded with {MDB_RESOURCE_NAME}-<tier>-<i>.{EXTERNAL_DOMAIN} and will not resolve."
    )


@mark.e2e_vm_migration_shardedcluster_external_access
def test_no_security_in_cr(generated_cr: dict):
    """Auth is disabled -- the generated CR must not contain a security section."""
    spec = generated_cr.get("spec", {})
    assert (
        "security" not in spec
    ), f"Expected no security section for auth-disabled deployment, got: {spec.get('security')}"


@mark.e2e_vm_migration_shardedcluster_external_access
def test_external_access_not_generated(generated_cr: dict):
    """The import tool does not emit externalAccess; the customer opts into it.

    Pins current CLI behaviour so this fails loudly if migrate-to-mck gains an external-domain flag.
    """
    spec = generated_cr["spec"]
    assert (
        "externalAccess" not in spec
    ), f"migrate-to-mck should not emit externalAccess, got: {spec.get('externalAccess')}"
    for component in ("configSrv", "shard", "mongos"):
        component_spec = spec.get(component, {})
        assert (
            "externalAccess" not in component_spec
        ), f"migrate-to-mck should not emit {component}.externalAccess, got: {component_spec.get('externalAccess')}"


@mark.e2e_vm_migration_shardedcluster_external_access
def test_no_user_crs_emitted(generated_cr_yaml: str):
    """Without auth, migrate must not produce any MongoDBUser documents."""
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 0, f"Expected 0 user CRs, got {len(user_docs)}"


# Lifecycle checks


@mark.e2e_vm_migration_shardedcluster_external_access
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """The dry-run probes every externalMember, not just mongos, so all three tiers are checked."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_external_services_created(namespace: str, mdb_migration: MongoDB):
    """Every member of every tier gets a LoadBalancer service on the IP CoreDNS was seeded with.

    Asserting IP equality identifies a MetalLB allocation shift as the cause of failure here. Two
    things can shift it: the tier creation order in createKubernetesResources changing, or another
    LoadBalancer service being live in the kind cluster and taking .200 first -- MetalLB hands out the
    lowest free address in 172.18.255.200-250.
    """
    for service_name, expected_ip in zip(_external_service_names_in_allocation_order(), _predicted_lb_ips()):
        service = get_service(namespace, service_name)
        assert service is not None, f"expected external service {service_name!r} to exist"
        assert service.spec.type == "LoadBalancer", f"{service_name} should be a LoadBalancer, got {service.spec.type}"

        ingress = service.status.load_balancer.ingress
        assert ingress, f"{service_name} has no LoadBalancer ingress assigned"
        assert ingress[0].ip == expected_ip, (
            f"{service_name} got IP {ingress[0].ip}, expected {expected_ip} (the address CoreDNS was seeded with). "
            f"Either the tier creation order changed (expected config, shards, mongos) or another "
            f"LoadBalancer service consumed an address from the 172.18.255.200-250 pool first."
        )


@mark.e2e_vm_migration_shardedcluster_external_access
def test_ac_hostnames_are_external(om_tester: OMTester, mdb_migration: MongoDB):
    """All three tiers' K8s members are published under the external domain, VM members are not."""
    ac_tester = om_tester.get_automation_config_tester()
    hostnames = [process["hostname"] for process in ac_tester.get_all_processes()]

    for tier in ("configSrv", "shard", "mongos"):
        for fqdn in k8s_tier_hostnames(mdb_migration, tier):
            assert (
                fqdn in hostnames
            ), f"expected external {tier} hostname {fqdn!r} in automation config, got: {hostnames}"

    k8s_internal = [h for h in hostnames if h.startswith(f"{MDB_RESOURCE_NAME}-") and ".svc.cluster.local" in h]
    assert not k8s_internal, f"k8s members must not be published under internal hostnames, got: {k8s_internal}"

    # The VM members legitimately keep their internal FQDNs -- the realistic mixed state.
    vm_internal = [h for h in hostnames if h.startswith("vm-sharded-") and ".svc.cluster.local" in h]
    assert vm_internal, f"expected VM members to keep internal FQDNs, got: {hostnames}"


@mark.e2e_vm_migration_shardedcluster_external_access
def test_connection_string_uses_external_domain(mdb_migration: MongoDB):
    assert_sharded_connection_string_uses_external_domain(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_external_access
@skip_if_local()
def test_connectivity_after_migration(mdb_migration: MongoDB):
    """Reachable through the external mongos hostnames, without authentication."""
    external_mongos_tester(mdb_migration).assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_external_access
@skip_if_local()
def test_migration_data_exists_after_migration(mdb_migration: MongoDB):
    assert_migration_data_exists(external_mongos_tester(mdb_migration))


@mark.e2e_vm_migration_shardedcluster_external_access
@skip_if_local()
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_external_access
def test_promote_and_prune_config_server(mdb_migration: MongoDB, om_tester: OMTester):
    """VM and K8s members of the same config server replica set talking over external names."""
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


@mark.e2e_vm_migration_shardedcluster_external_access
def test_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    promote_and_prune_shard(mdb_migration, om_tester, VM_SHARD_RS_NAME, VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration, external=True)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_connection_string_external_only_after_full_migration(mdb_migration: MongoDB):
    """Once the VM members are pruned, no internal hostname remains in either connection string."""
    assert_sharded_connection_string_uses_external_domain(mdb_migration, fully_migrated=True)


@mark.e2e_vm_migration_shardedcluster_external_access
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_external_access
@skip_if_local()
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_external_access
@skip_if_local()
def test_connectivity_after_promote(mdb_migration: MongoDB):
    """Still reachable through external mongos hostnames after promote and prune."""
    external_mongos_tester(mdb_migration).assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_external_access
@skip_if_local()
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(external_mongos_tester(mdb_migration))
