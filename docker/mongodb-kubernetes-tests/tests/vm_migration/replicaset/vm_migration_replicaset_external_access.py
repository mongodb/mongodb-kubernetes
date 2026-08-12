"""
VM migration to a Kubernetes replica set addressed through an external domain.

Simulates a production environment where operator-created pods are not reachable from outside the
cluster under their internal *.svc.cluster.local hostnames: the migrated members are exposed with
per-pod LoadBalancer services and published as <pod>.mongodb.interconnected.

MetalLB assigns LoadBalancer IPs from 172.18.255.200 upwards on kind (see
scripts/dev/recreate_kind_clusters.sh), so the IPs are predicted and seeded into CoreDNS before the
resource is applied. Related externalDomain tests: replicaset/replica_set_process_hostnames.py,
tls/tls_replica_set_process_hostnames.py.
"""

from kubetester import get_service, get_statefulset
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version, skip_if_local
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
from tests.vm_migration.vm_migration_replicaset_helper import (
    MIN_K8S_MONGOD,
    MIN_VM_MONGOD,
    apply_generated_mongodb_resource,
    assert_common_generated_cr_shape,
    assert_connection_string_after_full_migration,
    assert_connection_string_contains_current_hosts,
    assert_connection_string_uses_external_domain,
    assert_k8s_process_names,
    deploy_vm_service,
    deploy_vm_statefulset,
    external_replica_set_tester,
    promote_and_prune,
    vm_replica_set_tester,
)

RS_NAME = "vm-mongodb-rs"

EXTERNAL_DOMAIN = default_external_domain()
LB_IP_BASE = "172.18.255.200"


def _predicted_lb_ips() -> list[str]:
    """MetalLB assigns LoadBalancer IPs sequentially from 172.18.255.200 on single-cluster kind."""
    first, last_octet = LB_IP_BASE.rsplit(".", 1)
    # Mirrors apply_generated_mongodb_resource's member count: max(external_count, MIN_K8S_MONGOD).
    return [f"{first}.{int(last_octet) + i}" for i in range(max(MIN_VM_MONGOD, MIN_K8S_MONGOD))]


def _external_fqdns() -> list[str]:
    # Mirrors apply_generated_mongodb_resource's member count: max(external_count, MIN_K8S_MONGOD).
    return [f"{RS_NAME}-{i}.{EXTERNAL_DOMAIN}" for i in range(max(MIN_VM_MONGOD, MIN_K8S_MONGOD))]


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _configure_ac_no_auth(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    """Set up a replica set with auth DISABLED, port 27017, and compression."""
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["auth"] = {"disabled": True, "authoritativeSet": False}

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["replicaSets"] = [{"_id": rs_name, "members": [], "protocolVersion": "1"}]

    for i in range(vm_sts["spec"]["replicas"]):
        hostname = f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local"

        ac["monitoringVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        ac["processes"].append(
            {
                "version": mdb_version,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {
                        "port": 27017,
                        "tls": {"mode": "disabled"},
                        "compression": {"compressors": "snappy,zstd"},
                    },
                    "storage": {
                        "dbPath": "/data/",
                        "directoryPerDB": True,
                    },
                    "systemLog": {
                        "path": "/data/mongodb.log",
                        "destination": "file",
                    },
                    "replication": {"replSetName": rs_name},
                },
            }
        )

        ac["replicaSets"][0]["members"].append(
            {
                "_id": i + 100,
                "host": f"{sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    """Raw stdout from migrate (no SCRAM users, so no secrets needed)."""
    return run_generate_cr(namespace)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr: dict) -> MongoDB:
    def add_external_access(resource_doc: dict):
        # migrate-to-mck does not emit externalAccess, so the customer adds it when expanding
        # into Kubernetes. See test_external_access_not_generated.
        resource_doc["spec"]["externalAccess"] = {
            "externalDomain": EXTERNAL_DOMAIN,
            "externalService": {
                "spec": {
                    "type": "LoadBalancer",
                    "ports": [{"name": "mongodb", "port": 27017}],
                }
            },
        }

    return apply_generated_mongodb_resource(
        namespace,
        generated_cr,
        customer_sets_disabled_tls_mode=True,
        prepare_external_resources=add_external_access,
    )


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(external_replica_set_tester(mdb_migration))


# Test flow


@mark.e2e_vm_migration_replicaset_external_access
def test_update_coredns():
    """Seed CoreDNS before the resource exists, so external FQDNs resolve as soon as the LBs appear."""
    update_coredns_hosts(list(zip(_predicted_lb_ips(), _external_fqdns())))


@mark.e2e_vm_migration_replicaset_external_access
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_replicaset_external_access
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac_no_auth(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_replicaset_external_access
@skip_if_local()
def test_connectivity_before_migration(namespace: str):
    """Replica set is reachable without authentication before migration."""
    vm_replica_set_tester(namespace).assert_connectivity()


@mark.e2e_vm_migration_replicaset_external_access
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_vm_migration_replicaset_external_access
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_replica_set_tester(namespace))


# Generated CR checks


@mark.e2e_vm_migration_replicaset_external_access
def test_common_generated_cr_shape(generated_cr_yaml: str, generated_cr: dict, vm_sts: dict, version_id: str):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, version_id, vm_sts["spec"]["replicas"])


@mark.e2e_vm_migration_replicaset_external_access
def test_generated_resource_name_matches_expectation(generated_cr: dict):
    """CoreDNS is seeded from RS_NAME before the CR exists, so the generated name must match."""
    actual = generated_cr["metadata"]["name"]
    assert actual == RS_NAME, (
        f"expected generated resource name {RS_NAME!r}, got {actual!r}. "
        f"CoreDNS was seeded with {RS_NAME}-<i>.{EXTERNAL_DOMAIN} and will not resolve."
    )


@mark.e2e_vm_migration_replicaset_external_access
def test_no_security_in_cr(generated_cr: dict):
    """Auth is disabled -- the generated CR must not contain a security section."""
    spec = generated_cr.get("spec", {})
    assert (
        "security" not in spec
    ), f"Expected no security section for auth-disabled deployment, got: {spec.get('security')}"


@mark.e2e_vm_migration_replicaset_external_access
def test_external_access_not_generated(generated_cr: dict):
    """The import tool does not emit externalAccess; the customer opts into it.

    Pins current CLI behaviour so this fails loudly if migrate-to-mck gains an external-domain flag.
    """
    assert (
        "externalAccess" not in generated_cr["spec"]
    ), f"migrate-to-mck should not emit externalAccess, got: {generated_cr['spec'].get('externalAccess')}"


@mark.e2e_vm_migration_replicaset_external_access
def test_no_user_crs_emitted(generated_cr_yaml: str):
    """Without auth, migrate must not emit any MongoDBUser documents."""
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 0, f"Expected 0 user CRs, got {len(user_docs)}"


@mark.e2e_vm_migration_replicaset_external_access
def test_additional_mongod_config(generated_cr: dict):
    """additionalMongodConfig must reflect the net.compression.compressors and storage settings."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    assert (
        amc.get("net", {}).get("compression", {}).get("compressors") == "snappy,zstd"
    ), f"Expected compressors 'snappy,zstd', got: {amc}"
    assert amc.get("storage", {}).get("directoryPerDB") is True, f"Expected directoryPerDB=true, got: {amc}"


@mark.e2e_vm_migration_replicaset_external_access
def test_version_set(generated_cr: dict, custom_mdb_version: str):
    """spec.version must match the MongoDB version used in the AC."""
    assert generated_cr["spec"]["version"] == ensure_ent_version(custom_mdb_version)


@mark.e2e_vm_migration_replicaset_external_access
def test_agent_config(generated_cr: dict):
    """Agent config must include logRotate and systemLog from the (uniform) process config."""
    agent = generated_cr["spec"].get("agent", {}).get("mongod", {})
    lr = agent.get("logRotate", {})
    assert (
        lr.get("sizeThresholdMB") == "1000" or lr.get("sizeThresholdMB") == 1000
    ), f"Expected logRotate.sizeThresholdMB=1000, got: {lr}"
    sl = agent.get("systemLog", {})
    assert sl.get("destination") == "file", f"Expected systemLog.destination=file, got: {sl}"
    assert sl.get("path") == "/data/mongodb.log", f"Expected systemLog.path, got: {sl}"


# Lifecycle checks


@mark.e2e_vm_migration_replicaset_external_access
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Operator validates connectivity to all externalMembers, then the annotation is removed."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_external_access
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_external_access
def test_external_services_created(namespace: str, mdb_migration: MongoDB):
    """Each member gets a LoadBalancer service on the IP CoreDNS was seeded with.

    Asserting the IP equality identifies a MetalLB allocation shift as the cause of failure here.
    This test runs after test_migrate_vm_to_kubernetes, which already waits (timeout=1200) for the
    resource to reach Phase.Running, so a MetalLB drift will already have failed that wait; this
    assertion narrows down the cause rather than pre-empting a slow failure.

    NOTE: moving this check earlier -- polling for the LoadBalancer ingress IP before the phase
    wait -- would catch drift sooner. Not done here without a CI run to validate the reordering.
    """
    for i, expected_ip in enumerate(_predicted_lb_ips()):
        service_name = f"{RS_NAME}-{i}-svc-external"
        service = get_service(namespace, service_name)
        assert service is not None, f"expected external service {service_name!r} to exist"
        assert service.spec.type == "LoadBalancer", f"{service_name} should be a LoadBalancer, got {service.spec.type}"

        ingress = service.status.load_balancer.ingress
        assert ingress, f"{service_name} has no LoadBalancer ingress assigned"
        assert ingress[0].ip == expected_ip, (
            f"{service_name} got IP {ingress[0].ip}, but CoreDNS was seeded with {expected_ip}. "
            f"MetalLB allocation order changed -- update LB_IP_BASE or the prediction."
        )


@mark.e2e_vm_migration_replicaset_external_access
def test_ac_hostnames_are_external(om_tester: OMTester, mdb_migration: MongoDB, vm_sts):
    """K8s members are published under the external domain; VM members keep their internal FQDNs."""
    ac_tester = om_tester.get_automation_config_tester()
    hostnames = [process["hostname"] for process in ac_tester.get_all_processes()]

    for fqdn in _external_fqdns():
        assert fqdn in hostnames, f"expected external hostname {fqdn!r} in automation config, got: {hostnames}"

    vm_sts_name = vm_sts["metadata"]["name"]
    for i in range(vm_sts["spec"]["replicas"]):
        vm_hostname = f"{vm_sts_name}-{i}.{vm_sts_name}.{mdb_migration.namespace}.svc.cluster.local"
        assert vm_hostname in hostnames, f"expected VM hostname {vm_hostname!r} in automation config"

    k8s_internal = [h for h in hostnames if h.startswith(f"{RS_NAME}-") and ".svc.cluster.local" in h]
    assert not k8s_internal, f"k8s members must not be published under internal hostnames, got: {k8s_internal}"


@mark.e2e_vm_migration_replicaset_external_access
def test_connection_string_uses_external_domain(mdb_migration: MongoDB):
    assert_connection_string_uses_external_domain(mdb_migration)


@mark.e2e_vm_migration_replicaset_external_access
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_replicaset_external_access
@skip_if_local()
def test_connectivity_after_migration(mdb_migration: MongoDB):
    """Members are reachable through their external hostnames, without authentication."""
    external_replica_set_tester(mdb_migration).assert_connectivity()


@mark.e2e_vm_migration_replicaset_external_access
@skip_if_local()
def test_migration_data_exists_after_migration(mdb_migration: MongoDB):
    assert_migration_data_exists(external_replica_set_tester(mdb_migration))


@mark.e2e_vm_migration_replicaset_external_access
@skip_if_local()
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_replicaset_external_access
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_external_access
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_external_access
def test_connection_string_external_only_after_full_migration(mdb_migration: MongoDB):
    """Once the VM members are pruned, no internal hostname remains in either connection string."""
    assert_connection_string_uses_external_domain(mdb_migration, fully_migrated=True)


@mark.e2e_vm_migration_replicaset_external_access
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_external_access
@skip_if_local()
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_replicaset_external_access
@skip_if_local()
def test_connectivity_after_promote(mdb_migration: MongoDB):
    """Members remain reachable through external hostnames after promote and prune."""
    external_replica_set_tester(mdb_migration).assert_connectivity()


@mark.e2e_vm_migration_replicaset_external_access
@skip_if_local()
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(external_replica_set_tester(mdb_migration))
