"""Shared helpers for VM-to-Kubernetes migration tests."""

import difflib
import json

from kubetester.kubetester import fcv_from_version
from kubetester.omtester import OMTester
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


def configure_vm_replica_set(
    namespace: str,
    om_tester: OMTester,
    vm_sts: dict,
    vm_service: dict,
    mdb_version: str,
):
    """Register VM processes and a replica set in Ops Manager's automation config.

    Skips silently when processes already exist (test retry).
    Blocks until all agents reach goal state.
    """
    ac = om_tester.api_get_automation_config()

    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["replicaSets"] = [
        {"_id": rs_name, "members": [], "protocolVersion": "1"},
    ]

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
                    "net": {"port": 27017, "tls": {"mode": "disabled"}},
                    "storage": {"dbPath": "/data/"},
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                    "replication": {"replSetName": rs_name},
                },
            }
        )

        ac["replicaSets"][0]["members"].append(
            {
                "_id": i,
                "host": f"{sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )

    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


def log_automation_config(ac: dict, label: str = "current"):
    """Log the full automation config as pretty-printed JSON."""
    logger.info("Automation config [%s]:\n%s", label, json.dumps(ac, indent=2, sort_keys=True))


def log_automation_config_diff(ac_before: dict, ac_after: dict):
    """Log a unified diff of two automation config snapshots."""
    before_lines = json.dumps(ac_before, indent=2, sort_keys=True).splitlines(keepends=True)
    after_lines = json.dumps(ac_after, indent=2, sort_keys=True).splitlines(keepends=True)
    diff = list(difflib.unified_diff(before_lines, after_lines, fromfile="ac_before", tofile="ac_after"))
    if diff:
        logger.info("Automation config diff:\n%s", "".join(diff))
    else:
        logger.info("No changes in automation config.")
