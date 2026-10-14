"""Ops Manager project with its own project-scoped (GROUP_OWNER) API key, as a tenant of a shared Ops Manager has it.

The key has no global role, so everything done with it shows what the operator can do without GLOBAL_* permissions.
"""

from dataclasses import dataclass
from typing import Optional

import kubernetes.client
from kubetester import create_or_update_configmap, create_or_update_secret
from kubetester.kubetester import KubernetesTester
from kubetester.omtester import OMContext, OMTester

ASSIGNMENT_LABELS = ["tenant-label"]

SNAPSHOT_SCHEDULE = {
    "snapshotIntervalHours": 12,
    "snapshotRetentionDays": 3,
    "dailySnapshotRetentionDays": 5,
    "weeklySnapshotRetentionWeeks": 4,
    "monthlySnapshotRetentionMonths": 5,
}


@dataclass
class ScopedProject:
    name: str
    org_id: str
    group_id: str
    # a secret and a configmap that a MongoDB / MongoDBMultiCluster can reference as credentials and project
    secret_name: str
    config_name: str
    # talks to Ops Manager with the scoped key only, like the operator or a UI user of that project
    tester: OMTester


def create_scoped_project(
    admin: OMTester,
    namespace: str,
    name: str,
    base_url: str,
    ca_configmap: str,
    api_client: kubernetes.client.ApiClient,
) -> ScopedProject:
    """Creates an organization and a project and a GROUP_OWNER key for that project only, using the admin tester."""
    org = admin.om_request("post", "/orgs", json_object={"name": name}).json()
    group = admin.om_request("post", "/groups", json_object={"name": name, "orgId": org["id"]}).json()
    key = admin.om_request(
        "post",
        f"/groups/{group['id']}/apiKeys",
        json_object={"desc": "project scoped key for backup test", "roles": ["GROUP_OWNER"]},
    ).json()
    admin.om_request(
        "post",
        f"/orgs/{org['id']}/apiKeys/{key['id']}/accessList",
        json_object=[{"cidrBlock": "0.0.0.0/1"}, {"cidrBlock": "128.0.0.0/1"}],
    )

    secret_name = f"{name}-credentials"
    create_or_update_secret(
        namespace,
        secret_name,
        {"publicKey": key["publicKey"], "privateKey": key["privateKey"]},
        api_client=api_client,
    )
    config_name = f"{name}-config"
    create_or_update_configmap(
        namespace,
        config_name,
        {"baseUrl": base_url, "projectName": name, "orgId": org["id"], "sslMMSCAConfigMap": ca_configmap},
        api_client=api_client,
    )

    return ScopedProject(
        name=name,
        org_id=org["id"],
        group_id=group["id"],
        secret_name=secret_name,
        config_name=config_name,
        tester=OMTester(
            OMContext(
                admin.context.base_url,
                key["publicKey"],
                key["privateKey"],
                project_name=name,
                project_id=group["id"],
                org_id=org["id"],
            )
        ),
    )


def backup_config_status(project: ScopedProject) -> Optional[str]:
    configs = project.tester.api_read_backup_configs()
    return configs[0]["statusName"] if configs else None


def wait_for_backup_config(project: ScopedProject, timeout: int = 300):
    """Ops Manager creates the backup config once the deployment is monitored."""
    KubernetesTester.wait_until(lambda: len(project.tester.api_read_backup_configs()) > 0, timeout=timeout)


def start_backup_like_the_ui(project: ScopedProject, timeout: int = 300):
    """Starts the backup the way the Ops Manager UI does, with the project scoped key."""
    cluster_id = project.tester.get_backup_cluster_id()
    project.tester.om_request(
        "patch",
        f"/groups/{project.group_id}/backupConfigs/{cluster_id}",
        json_object={"statusName": "STARTED"},
    )
    KubernetesTester.wait_until(lambda: backup_config_status(project) == "STARTED", timeout=timeout)
