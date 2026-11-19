"""Starts backup of an Ops Manager project the way the Ops Manager UI does it, outside of the operator."""

from typing import Optional

from kubetester.kubetester import KubernetesTester
from kubetester.omtester import OMTester


def backup_config_status(project: OMTester) -> Optional[str]:
    configs = project.api_read_backup_configs()
    return configs[0]["statusName"] if configs else None


def wait_for_backup_config(project: OMTester, timeout: int = 300):
    """Ops Manager creates the backup config once the deployment is monitored."""
    KubernetesTester.wait_until(lambda: len(project.api_read_backup_configs()) > 0, timeout=timeout)


def start_backup_like_the_ui(project: OMTester, timeout: int = 300):
    cluster_id = project.get_backup_cluster_id()
    project.om_request(
        "patch",
        f"/groups/{project.context.project_id}/backupConfigs/{cluster_id}",
        json_object={"statusName": "STARTED"},
    )
    KubernetesTester.wait_until(lambda: backup_config_status(project) == "STARTED", timeout=timeout)
