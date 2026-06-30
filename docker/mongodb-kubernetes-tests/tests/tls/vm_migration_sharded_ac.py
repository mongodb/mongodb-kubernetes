"""Shared AC-building helper for VM sharded cluster migration tests.

Both vm_migration_sharded and vm_migration_sharded_x509 call build_sharded_cluster_ac
to produce the automation config dict that drives pseudo-VM agents toward goal state.
The helper lives here so it is not a method on OMTester (which owns only OM API calls).
"""

from typing import Optional

from kubetester.kubetester import fcv_from_version
from kubetester.omtester import OMTester


def build_sharded_cluster_ac(
    om_tester: OMTester,
    mongod_sts_name: str,
    mongos_sts_name: str,
    service_name: str,
    mongos_service_name: str,
    namespace: str,
    mongodb_version: str,
    config_rs_name: str,
    shard_rs_name: str,
    config_server_count: int = 3,
    shard_count: int = 3,
    mongos_count: int = 2,
    cluster_name: Optional[str] = None,
    tls: bool = False,
    mongod_cert_path: str = "/mongodb-automation/server.pem",
    mongos_cert_path: str = "/mongodb-automation/server.pem",
    ca_cert_path: str = "/mongodb-automation/tls/ca/ca-pem",
    agent_cert_path: str = "",
    x509_agent_subject_dn: str = "",
) -> dict:
    """Build an automation config dict for a pseudo-VM sharded cluster.

    Returns an AC with processes, replicaSets, and sharding entries. Does
    not PUT the config. Each process has net.tls.mode set to "disabled"
    unless tls=True, in which case requireTLS is used with the provided cert paths.

    When x509_agent_subject_dn is set, the AC auth block is configured for X509.

    The config server replica set uses pods 0..(config_server_count-1) from
    the mongod StatefulSet. The shard replica set uses pods
    config_server_count..(config_server_count+shard_count-1).
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

    def _mongod_process(pod_index: int, rs_name: str) -> dict:
        hostname = _fqdn(mongod_sts_name, pod_index, service_name)
        process_name = f"{mongod_sts_name}-{pod_index}"
        net = {"port": 27017}
        if tls:
            net["tls"] = {"mode": "requireTLS", "certificateKeyFile": mongod_cert_path}
        else:
            net["tls"] = {"mode": "disabled"}
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
                "storage": {"dbPath": "/data/"},
                "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                "replication": {"replSetName": rs_name},
            },
        }

    # Config server replica set processes (pod indices 0..config_server_count-1).
    config_rs_members = []
    for i in range(config_server_count):
        config_rs_members.append(
            {
                "_id": i,
                "host": f"{mongod_sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )
        process = _mongod_process(i, config_rs_name)
        process["args2_6"]["sharding"] = {"clusterRole": "configsvr"}
        ac["processes"].append(process)
        hostname = _fqdn(mongod_sts_name, i, service_name)
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    ac["replicaSets"].append(
        {
            "_id": config_rs_name,
            "members": config_rs_members,
            "protocolVersion": "1",
        }
    )

    # Shard replica set processes (pod indices config_server_count..config_server_count+shard_count-1).
    shard_rs_members = []
    for j in range(shard_count):
        pod_index = config_server_count + j
        shard_rs_members.append(
            {
                "_id": j,
                "host": f"{mongod_sts_name}-{pod_index}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )
        process = _mongod_process(pod_index, shard_rs_name)
        process["args2_6"]["sharding"] = {"clusterRole": "shardsvr"}
        ac["processes"].append(process)
        hostname = _fqdn(mongod_sts_name, pod_index, service_name)
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    ac["replicaSets"].append(
        {
            "_id": shard_rs_name,
            "members": shard_rs_members,
            "protocolVersion": "1",
        }
    )

    # Mongos processes.
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
            "usersWanted": [],
            "usersDeleted": [],
        }

    # Sharding entry wires the config server RS and the shard RS together.
    ac["sharding"].append(
        {
            "name": cluster_name,
            "configServerReplica": config_rs_name,
            "shards": [
                {
                    "_id": shard_rs_name,
                    "rs": shard_rs_name,
                    "tags": [],
                }
            ],
            "managedSharding": True,
        }
    )

    return ac
