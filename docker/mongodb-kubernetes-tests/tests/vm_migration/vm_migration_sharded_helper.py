"""Sharded-cluster-specific helpers for VM-to-Kubernetes migration tests.

Deploys the three VM sharded StatefulSets (config server, shard, mongos), builds the
automation config for a pseudo-VM sharded cluster, applies the generated MongoDB CR, and
asserts sharded process names and connectivity. Shared primitives live in
vm_migration_common_helper.
"""

from typing import Optional

import yaml
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoTester, build_mongodb_connection_uri
from kubetester.omtester import OMTester
from kubetester.phase import Phase
from tests.vm_migration.vm_migration_common_helper import (
    MIGRATION_DRY_RUN_ANNOTATION,
    _deploy_vm_statefulset_from_fixture,
    generated_mongodb_doc,
)

# The voting limit test trips the config server and a shard independently: the config server
# votes come from top-level spec.memberConfig and the shard votes from its own shardOverride,
# so making one component's K8s members voting never affects the other. Each K8s count plus
# its VM members is sized to cross the 7 voting member limit on its own. See
# assert_max_voting_members_validation for the arithmetic.
MIN_K8S_CONFIGSRV = 6
MIN_K8S_SHARD = 4
MIN_K8S_MONGOS = 2
MIN_VM_CONFIGSRV = 3
MIN_VM_SHARD = 4
MIN_VM_MONGOS = 2


def deploy_vm_sharded_configsrv_statefulset(
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args: str = "",
) -> dict:
    """Create or update the VM config server StatefulSet with OM credentials. Returns the body dict."""
    return _deploy_vm_statefulset_from_fixture(
        "vm_sharded_configsrv_statefulset.yaml",
        namespace,
        om_tester,
        extra_volumes=extra_volumes,
        extra_volume_mounts=extra_volume_mounts,
        extra_command_args=extra_command_args,
        replicas=MIN_VM_CONFIGSRV,
    )


def deploy_vm_sharded_shard_statefulset(
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args: str = "",
) -> dict:
    """Create or update the VM shard StatefulSet with OM credentials. Returns the body dict."""
    return _deploy_vm_statefulset_from_fixture(
        "vm_sharded_shard_statefulset.yaml",
        namespace,
        om_tester,
        extra_volumes=extra_volumes,
        extra_volume_mounts=extra_volume_mounts,
        extra_command_args=extra_command_args,
        replicas=MIN_VM_SHARD,
    )


def deploy_vm_sharded_mongos_statefulset(
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args: str = "",
) -> dict:
    """Create or update the VM mongos StatefulSet with OM credentials. Returns the body dict."""
    return _deploy_vm_statefulset_from_fixture(
        "vm_sharded_mongos_statefulset.yaml",
        namespace,
        om_tester,
        extra_volumes=extra_volumes,
        extra_volume_mounts=extra_volume_mounts,
        extra_command_args=extra_command_args,
        replicas=MIN_VM_MONGOS,
    )


def deploy_vm_sharded_configsrv_service(namespace: str) -> dict:
    """Create or update the VM config server headless service. Returns the body dict."""
    with open(yaml_fixture("vm_sharded_configsrv_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def deploy_vm_sharded_shard_service(namespace: str) -> dict:
    """Create or update the VM shard headless service. Returns the body dict."""
    with open(yaml_fixture("vm_sharded_shard_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def deploy_vm_sharded_mongos_service(namespace: str) -> dict:
    """Create or update the VM mongos headless service. Returns the body dict."""
    with open(yaml_fixture("vm_sharded_mongos_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def apply_generated_sharded_cluster_resource(
    namespace: str,
    generated_cr_yaml: str,
    config_rs_name: str,
    *,
    resource_name: str | None = None,
    customer_sets_disabled_tls_mode: bool = False,
    prepare_external_resources=None,
) -> MongoDB:
    """Apply the generated sharded cluster CR. The config server K8s members get their votes from
    top-level memberConfig and each shard gets its own shardOverride, both non voting to start."""
    resource_doc = generated_mongodb_doc(generated_cr_yaml)
    resource = MongoDB(resource_name or resource_doc["metadata"]["name"], namespace)
    if try_load(resource):
        return resource

    if customer_sets_disabled_tls_mode:
        for component in ("configSrv", "shard", "mongos"):
            resource_doc["spec"].setdefault(component, {}).setdefault("additionalMongodConfig", {}).setdefault(
                "net", {}
            ).setdefault("tls", {})["mode"] = "disabled"

    # The generated CR carries all Kubernetes node counts at 0, mirroring the replica set Members
    # field, so the customer sets the target Kubernetes counts here. The VM nodes stay in
    # externalMembers and the Kubernetes members scale up from 0.
    resource_doc["spec"]["mongodsPerShardCount"] = MIN_K8S_SHARD
    resource_doc["spec"]["mongosCount"] = MIN_K8S_MONGOS

    # Shard K8s members default to voting, which would already put a shard over the limit once its
    # VM members are counted. A per-shard override pins them non voting and keeps shard votes
    # independent of the config server for the voting limit test.
    resource_name_value = resource_doc["metadata"]["name"]
    resource_doc["spec"]["shardOverrides"] = [
        {
            "shardNames": [f"{resource_name_value}-{shard_index}"],
            "memberConfig": [{"votes": 0, "priority": "0"} for _ in range(MIN_K8S_SHARD)],
        }
        for shard_index in range(resource_doc["spec"]["shardCount"])
    ]

    config_members = [
        m for m in resource_doc["spec"].get("externalMembers", []) if m.get("replicaSetName") == config_rs_name
    ]
    if config_members:
        resource_doc["spec"]["configServerCount"] = MIN_K8S_CONFIGSRV
        resource_doc["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(MIN_K8S_CONFIGSRV)]

    if prepare_external_resources is not None:
        prepare_external_resources(resource_doc)

    resource.backing_obj = resource_doc
    resource.update()
    return resource


def assert_connection_string_after_full_sharded_migration(mdb_migration: MongoDB, ca_path: str | None = None) -> None:
    """After all external members are pruned, assert the sharded cluster is reachable.

    Pass ca_path for TLS-enforced clusters so the connectivity check uses a TLS client;
    without it a plaintext client can never connect and assert_connectivity times out.
    """
    assert not mdb_migration["spec"].get("externalMembers"), "expected all external members to be pruned by now"
    mdb_migration.tester(use_ssl=ca_path is not None, ca_path=ca_path).assert_connectivity()


def assert_common_generated_sharded_cr_shape(
    generated_cr: dict,
    expected_config_count: int,
    expected_shard_count: int,
    expected_mongos_count: int,
) -> None:
    """Assert the generated sharded CR has the expected externalMembers and dry-run annotation."""
    assert generated_cr.get("kind") == "MongoDB", f"Expected kind=MongoDB, got: {generated_cr.get('kind')}"

    spec = generated_cr.get("spec", {})
    assert "externalMembers" in spec, "externalMembers missing from generated CR"

    external_members = spec["externalMembers"]
    expected_total = expected_config_count + expected_shard_count + expected_mongos_count
    assert (
        len(external_members) == expected_total
    ), f"Expected {expected_total} externalMembers, got {len(external_members)}"
    for m in external_members:
        for key in ("processName", "hostname", "type"):
            assert key in m, f"externalMember missing key '{key}': {m}"
        assert m["type"] in ("mongod", "mongos"), f"Unexpected type in externalMember: {m['type']}"
        if m["type"] == "mongod":
            assert "replicaSetName" in m, f"externalMember of type mongod missing 'replicaSetName': {m}"

    annotations = generated_cr.get("metadata", {}).get("annotations", {})
    assert MIGRATION_DRY_RUN_ANNOTATION in annotations, "dry-run annotation missing from generated CR"


def assert_k8s_sharded_process_names(om_tester: OMTester, mdb_migration: MongoDB) -> None:
    """Assert all K8s sharded cluster process names appear in the automation config."""
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [p["name"] for p in ac_tester.get_all_processes()]
    name = mdb_migration.name
    ns = mdb_migration.namespace
    for i in range(mdb_migration["spec"].get("configServerCount", MIN_K8S_CONFIGSRV)):
        assert f"k8s/{ns}/{name}-config-{i}" in process_names
    shard_count = mdb_migration["spec"]["shardCount"]
    mongods_per_shard = mdb_migration["spec"].get("mongodsPerShardCount", MIN_K8S_SHARD)
    for shard in range(shard_count):
        for i in range(mongods_per_shard):
            assert f"k8s/{ns}/{name}-{shard}-{i}" in process_names
    for i in range(mdb_migration["spec"].get("mongosCount", MIN_K8S_MONGOS)):
        assert f"k8s/{ns}/{name}-mongos-{i}" in process_names


def vm_mongos_tester(
    mongos_sts_name: str, mongos_svc_name: str, namespace: str, ca_path: str | None = None
) -> MongoTester:
    """Return a MongoTester pointed at the first VM mongos pod."""
    uri = build_mongodb_connection_uri(
        mdb_resource=mongos_sts_name,
        namespace=namespace,
        members=1,
        port="27017",
        servicename=mongos_svc_name,
    )
    return MongoTester(uri, use_ssl=ca_path is not None, ca_path=ca_path)


def build_sharded_cluster_ac(
    om_tester: OMTester,
    configsrv_sts_name: str,
    shard_sts_name: str,
    mongos_sts_name: str,
    configsrv_service_name: str,
    shard_service_name: str,
    mongos_service_name: str,
    namespace: str,
    mongodb_version: str,
    config_rs_name: str,
    shard_rs_name: str,
    config_server_count: int = MIN_VM_CONFIGSRV,
    shard_count: int = MIN_VM_SHARD,
    mongos_count: int = MIN_VM_MONGOS,
    cluster_name: Optional[str] = None,
    tls: bool = False,
    mongod_cert_path: str = "/mongodb-automation/server.pem",
    mongos_cert_path: str = "/mongodb-automation/server.pem",
    ca_cert_path: str = "/mongodb-automation/tls/ca/ca-pem",
    agent_cert_path: str = "",
    x509_agent_subject_dn: str = "",
    compressors: Optional[str] = None,
    directory_per_db: bool = False,
) -> dict:
    """Build an automation config dict for a pseudo-VM sharded cluster.

    Returns an AC with processes, replicaSets, and sharding entries. Does
    not PUT the config. Each process has net.tls.mode set to "disabled"
    unless tls=True, in which case requireTLS is used with the provided cert paths.

    The config server replica set uses pods 0..(config_server_count-1) from the
    config server StatefulSet. The shard replica set uses pods
    0..(shard_count-1) from the shard StatefulSet.
    """
    if cluster_name is None:
        cluster_name = config_rs_name[: -len("-config")] if config_rs_name.endswith("-config") else config_rs_name

    ac = om_tester.api_get_automation_config()
    ac["processes"] = []
    ac["replicaSets"] = []
    ac["sharding"] = []
    ac["monitoringVersions"] = []
    ac["backupVersions"] = []

    def _fqdn(sts: str, pod_index: int, svc: str) -> str:
        return f"{sts}-{pod_index}.{svc}.{namespace}.svc.cluster.local"

    def _monitoring_entry(hostname: str) -> dict:
        entry = {
            "hostname": hostname,
            "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
        }
        if tls:
            entry["additionalParams"] = {
                "sslTrustedServerCertificates": ca_cert_path,
                "useSslForAllConnections": "true",
            }
        return entry

    def _backup_entry(hostname: str) -> dict:
        return {
            "hostname": hostname,
            "logPath": "/var/log/mongodb-mms-automation/backup-agent.log",
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
        }

    def _mongod_process(sts_name: str, svc_name: str, pod_index: int, rs_name: str) -> dict:
        hostname = _fqdn(sts_name, pod_index, svc_name)
        process_name = f"{sts_name}-{pod_index}"
        net = {"port": 27017}
        if tls:
            net["tls"] = {"mode": "requireTLS", "certificateKeyFile": mongod_cert_path}
        else:
            net["tls"] = {"mode": "disabled"}
        if compressors:
            net["compression"] = {"compressors": compressors}
        storage = {"dbPath": "/data/"}
        if directory_per_db:
            storage["directoryPerDB"] = True
        return {
            "version": mongodb_version,
            "name": process_name,
            "hostname": hostname,
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            "authSchemaVersion": 5,
            "featureCompatibilityVersion": fcv_from_version(mongodb_version),
            "processType": "mongod",
            "args2_6": {
                "net": net,
                "storage": storage,
                "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                "replication": {"replSetName": rs_name},
            },
        }

    config_rs_members = []
    for i in range(config_server_count):
        config_rs_members.append(
            {
                "_id": i,
                "host": f"{configsrv_sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )
        process = _mongod_process(configsrv_sts_name, configsrv_service_name, i, config_rs_name)
        process["args2_6"]["sharding"] = {"clusterRole": "configsvr"}
        ac["processes"].append(process)
        hostname = _fqdn(configsrv_sts_name, i, configsrv_service_name)
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    ac["replicaSets"].append({"_id": config_rs_name, "members": config_rs_members, "protocolVersion": "1"})

    shard_rs_members = []
    for j in range(shard_count):
        shard_rs_members.append(
            {
                "_id": j,
                "host": f"{shard_sts_name}-{j}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )
        process = _mongod_process(shard_sts_name, shard_service_name, j, shard_rs_name)
        process["args2_6"]["sharding"] = {"clusterRole": "shardsvr"}
        ac["processes"].append(process)
        hostname = _fqdn(shard_sts_name, j, shard_service_name)
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    ac["replicaSets"].append({"_id": shard_rs_name, "members": shard_rs_members, "protocolVersion": "1"})

    for k in range(mongos_count):
        hostname = _fqdn(mongos_sts_name, k, mongos_service_name)
        process_name = f"{mongos_sts_name}-{k}"
        mongos_net = {"port": 27017}
        if tls:
            mongos_net["tls"] = {"mode": "requireTLS", "certificateKeyFile": mongos_cert_path}
        else:
            mongos_net["tls"] = {"mode": "disabled"}
        ac["processes"].append(
            {
                "version": mongodb_version,
                "name": process_name,
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mongodb_version),
                "processType": "mongos",
                "args2_6": {
                    "net": mongos_net,
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                },
                "cluster": cluster_name,
            }
        )
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    if tls:
        tls_block: dict = {
            "CAFilePath": ca_cert_path,
            "clientCertificateMode": "REQUIRE" if x509_agent_subject_dn else "OPTIONAL",
        }
        if agent_cert_path:
            tls_block["autoPEMKeyFilePath"] = agent_cert_path
        ac["tls"] = tls_block

    if x509_agent_subject_dn:
        ac["auth"] = {
            "disabled": False,
            "authoritativeSet": True,
            "autoUser": x509_agent_subject_dn,
            "autoAuthMechanism": "MONGODB-X509",
            "autoAuthMechanisms": ["MONGODB-X509"],
            "autoAuthRestrictions": [],
            "deploymentAuthMechanisms": ["MONGODB-X509"],
            "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
            "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
            "key": "dGVzdC1rZXlmaWxlLWNvbnRlbnQtZm9yLXZtLW1pZ3JhdGlvbi14NTA5",
            "usersWanted": [
                {
                    "user": x509_agent_subject_dn,
                    "db": "$external",
                    "roles": [{"role": "root", "db": "admin"}],
                    "mechanisms": [],
                    "scramSha256Creds": None,
                    "scramSha1Creds": None,
                    "authenticationRestrictions": [],
                }
            ],
            "usersDeleted": [],
        }

    ac["sharding"].append(
        {
            "name": cluster_name,
            "configServerReplica": config_rs_name,
            "shards": [{"_id": shard_rs_name, "rs": shard_rs_name, "tags": []}],
            "managedSharding": True,
        }
    )

    return ac


def promote_and_prune_shard(
    mdb_migration: MongoDB,
    om_tester: OMTester,
    vm_shard_rs_name: str,
    mongos_cluster_name: str,
    timeout: int = 600,
) -> None:
    """Promote each Kubernetes shard member and prune one VM shard member at a time.

    Shard member votes live in the shard's own shardOverride, so they must be promoted explicitly
    here. Otherwise pruning the voting VM members would leave the shard with only non voting
    Kubernetes members, which is not a valid replica set.
    """
    try_load(mdb_migration)
    shard_k8s_name = _shard_k8s_name_for_rs(mdb_migration, vm_shard_rs_name)
    vm_shard_count = len(
        [m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == vm_shard_rs_name]
    )
    for i in range(vm_shard_count):
        override = next(o for o in mdb_migration["spec"]["shardOverrides"] if shard_k8s_name in o["shardNames"])
        override["memberConfig"][i]["priority"] = "1"
        override["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=timeout)

        current = [m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == vm_shard_rs_name]
        if current:
            mdb_migration["spec"]["externalMembers"].remove(current[-1])
            mdb_migration.update()
            mdb_migration.assert_reaches_phase(Phase.Running, timeout=timeout)

        om_tester.assert_cluster_available(mongos_cluster_name)


def _shard_k8s_name_for_rs(mdb_migration: MongoDB, vm_shard_rs_name: str) -> str:
    """Return the Kubernetes shard name whose AC replica set name is vm_shard_rs_name.

    The AC name comes from shardNameOverrides and falls back to the Kubernetes name.
    """
    spec = mdb_migration["spec"]
    ac_names = {o["shardName"]: o.get("replicaSetName") for o in spec.get("shardNameOverrides", [])}
    for shard_index in range(spec["shardCount"]):
        k8s_name = f"{mdb_migration.name}-{shard_index}"
        if (ac_names.get(k8s_name) or k8s_name) == vm_shard_rs_name:
            return k8s_name
    raise AssertionError(f"no shard maps to replica set {vm_shard_rs_name}")
