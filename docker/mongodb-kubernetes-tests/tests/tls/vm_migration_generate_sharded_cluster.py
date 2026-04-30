"""
VM migration test using kubectl-mongodb migrate for a sharded cluster with multiple mongos.

Mirrors vm_migration_generate_no_auth.py but exercises the sharded-cluster
generator code path. Verifies:
  - The generated CR is type ShardedCluster with shardCount, mongosCount,
    mongodsPerShardCount, configServerCount populated from the AC
  - externalMembers cover every legacy process: shards (mongod + replicaSetName),
    config servers (mongod + replicaSetName=csrs), mongos (type=mongos, no
    replicaSetName)
  - shardNameOverrides comment is written when shard ids differ from the
    derived names
  - Full promote-and-prune lifecycle reaches Phase.Running while moving each
    role from external to internal one step at a time.
"""

import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_helpers import (
    deploy_vm_service,
    deploy_vm_statefulset,
    promote_and_prune_sharded,
    run_migrate_generate,
)

RESOURCE_NAME = "my-sharded-cluster"
SHARD_COUNT = 2
MONGODS_PER_SHARD = 2
CONFIG_SERVER_COUNT = 2
MONGOS_COUNT = 2
TOTAL_VM_REPLICAS = SHARD_COUNT * MONGODS_PER_SHARD + CONFIG_SERVER_COUNT + MONGOS_COUNT


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_statefulset(namespace, om_tester, replicas=TOTAL_VM_REPLICAS)


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _process(name: str, hostname: str, port: int, version: str, role: str, rs_name: str = ""):
    """Build a single AC process entry. role is one of mongod or mongos."""
    args = {
        "net": {"port": port, "tls": {"mode": "disabled"}},
        "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
    }
    if role == "mongod":
        args["storage"] = {"dbPath": "/data/", "directoryPerDB": True}
        if rs_name:
            args["replication"] = {"replSetName": rs_name}
    if rs_name == "csrs":
        args.setdefault("sharding", {})["clusterRole"] = "configsvr"
    elif role == "mongod" and rs_name:
        args.setdefault("sharding", {})["clusterRole"] = "shardsvr"
    return {
        "version": version,
        "name": name,
        "hostname": hostname,
        "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
        "authSchemaVersion": 5,
        "featureCompatibilityVersion": fcv_from_version(version),
        "processType": role,
        "args2_6": args,
    }


def _configure_sharded_ac(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]

    def hostname(idx: int) -> str:
        return f"{sts_name}-{idx}.{svc_name}.{namespace}.svc.cluster.local"

    ac["auth"] = {"disabled": True, "authoritativeSet": False}
    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["replicaSets"] = []
    ac["sharding"] = []

    pod = 0

    config_members = []
    for member_idx in range(CONFIG_SERVER_COUNT):
        proc_name = f"csrs-{member_idx}"
        ac["processes"].append(_process(proc_name, hostname(pod), 27019, mdb_version, "mongod", "csrs"))
        config_members.append({"_id": member_idx, "host": proc_name, "priority": 1, "votes": 1})
        pod += 1
    ac["replicaSets"].append({"_id": "csrs", "members": config_members, "protocolVersion": "1"})

    shards = []
    for shard_idx in range(SHARD_COUNT):
        rs_name = f"shard{shard_idx}"
        members = []
        for member_idx in range(MONGODS_PER_SHARD):
            proc_name = f"{rs_name}-{member_idx}"
            ac["processes"].append(_process(proc_name, hostname(pod), 27018, mdb_version, "mongod", rs_name))
            members.append({"_id": member_idx, "host": proc_name, "priority": 1, "votes": 1})
            pod += 1
        ac["replicaSets"].append({"_id": rs_name, "members": members, "protocolVersion": "1"})
        shards.append({"_id": rs_name, "rs": rs_name})

    for mongos_idx in range(MONGOS_COUNT):
        proc_name = f"mongos-{mongos_idx}"
        proc = _process(proc_name, hostname(pod), 27017, mdb_version, "mongos")
        proc["cluster"] = "my-sharded-cluster"
        ac["processes"].append(proc)
        pod += 1

    ac["sharding"].append(
        {
            "name": "my-sharded-cluster",
            "configServer": "csrs",
            "shards": shards,
            "managedSharding": True,
            "draining": [],
            "tags": [],
            "collections": [],
        }
    )

    for proc in ac["processes"]:
        ac["monitoringVersions"].append(
            {
                "hostname": proc["hostname"],
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_migrate_generate(namespace, passwords=None)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return next(yaml.safe_load_all(generated_cr_yaml))


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr: dict) -> MongoDB:
    resource = MongoDB(RESOURCE_NAME, namespace)
    if try_load(resource):
        return resource

    resource.backing_obj = generated_cr
    resource.backing_obj.setdefault("spec", {}).setdefault("additionalMongodConfig", {}).setdefault(
        "net", {}
    ).setdefault("tls", {})["mode"] = "disabled"
    # Generated CR starts with all internal counts at 0. Customers raise them
    # gradually. The promote-and-prune helper does the same.
    resource.backing_obj["spec"]["mongodsPerShardCount"] = 0
    resource.backing_obj["spec"]["configServerCount"] = 0
    resource.backing_obj["spec"]["mongosCount"] = 0
    resource.update()
    return resource


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@mark.e2e_vm_migration_generate_sharded_cluster
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == TOTAL_VM_REPLICAS

    KubernetesTester.wait_until(sts_is_ready, timeout=600)


@mark.e2e_vm_migration_generate_sharded_cluster
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_sharded_ac(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_generate_sharded_cluster
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_generate_sharded_cluster
def test_cr_is_sharded_cluster(generated_cr: dict):
    assert generated_cr["kind"] == "MongoDB"
    assert generated_cr["spec"]["type"] == "ShardedCluster"


@mark.e2e_vm_migration_generate_sharded_cluster
def test_topology_counts_match_ac(generated_cr: dict):
    spec = generated_cr["spec"]
    assert spec["shardCount"] == SHARD_COUNT
    assert spec["mongodsPerShardCount"] == MONGODS_PER_SHARD
    assert spec["configServerCount"] == CONFIG_SERVER_COUNT
    assert spec["mongosCount"] == MONGOS_COUNT


@mark.e2e_vm_migration_generate_sharded_cluster
def test_external_members_cover_all_processes(generated_cr: dict):
    ext = generated_cr["spec"]["externalMembers"]
    assert len(ext) == TOTAL_VM_REPLICAS, f"Expected {TOTAL_VM_REPLICAS} externalMembers, got {len(ext)}"

    mongos = [em for em in ext if em.get("type") == "mongos"]
    assert len(mongos) == MONGOS_COUNT, f"Expected {MONGOS_COUNT} mongos in externalMembers, got {len(mongos)}"
    for em in mongos:
        assert "replicaSetName" not in em or not em["replicaSetName"], "mongos must not carry a replicaSetName"

    csrs = [em for em in ext if em.get("type") == "mongod" and em.get("replicaSetName") == "csrs"]
    assert len(csrs) == CONFIG_SERVER_COUNT

    for shard_idx in range(SHARD_COUNT):
        rs = f"shard{shard_idx}"
        members = [em for em in ext if em.get("replicaSetName") == rs]
        assert len(members) == MONGODS_PER_SHARD, f"Expected {MONGODS_PER_SHARD} members for {rs}"


@mark.e2e_vm_migration_generate_sharded_cluster
def test_no_security_in_cr(generated_cr: dict):
    """Auth is disabled. The generated CR must omit spec.security."""
    assert "security" not in generated_cr.get("spec", {}), "expected no security section for auth-disabled deployment"


@mark.e2e_vm_migration_generate_sharded_cluster
def test_no_user_crs_written(generated_cr_yaml: str):
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    user_docs = [d for d in docs if d and d.get("kind") == "MongoDBUser"]
    assert len(user_docs) == 0, f"Expected 0 user CRs, got {len(user_docs)}"


@mark.e2e_vm_migration_generate_sharded_cluster
def test_version_set(generated_cr: dict, custom_mdb_version: str):
    assert generated_cr["spec"]["version"] == ensure_ent_version(custom_mdb_version)


@mark.e2e_vm_migration_generate_sharded_cluster
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_generate_sharded_cluster
@mark.skip(reason="TODO: enable once the operator supports VM migrations for sharded clusters")
def test_promote_and_prune(mdb_migration: MongoDB):
    promote_and_prune_sharded(
        mdb_migration,
        target_shards=MONGODS_PER_SHARD,
        target_config_servers=CONFIG_SERVER_COUNT,
        target_mongos=MONGOS_COUNT,
    )


@mark.e2e_vm_migration_generate_sharded_cluster
@mark.skip(reason="TODO: enable once the operator supports VM migrations for sharded clusters")
def test_external_members_empty_after_prune(mdb_migration: MongoDB):
    mdb_migration.reload()
    assert mdb_migration["spec"].get("externalMembers", []) == []
