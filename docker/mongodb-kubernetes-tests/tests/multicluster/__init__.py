import os
from typing import Dict, List

from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.helm import helm_template
from kubetester.multicluster_client import MultiClusterClient
from tests import test_logger
from tests.constants import LOCAL_HELM_CHART_DIR

logger = test_logger.get_test_logger(__name__)


def prepare_multi_cluster_namespaces(
    namespace: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_name: str,
    skip_central_cluster: bool = True,
    helm_chart_path=LOCAL_HELM_CHART_DIR,
):
    """create a new namespace and configures all necessary service accounts there"""

    helm_args = multi_cluster_operator_installation_config
    logger.debug("Applying the following template to member clusters:")
    # Always use the local helm chart directory for rendering RBAC templates so that
    # changes to database-roles.yaml in the current build are applied to member clusters,
    # rather than the latest published OCI chart which may not include those changes.
    yaml_file = helm_template(
        helm_args=helm_args,
        templates="templates/database-roles.yaml",
        helm_options=[f"--namespace {namespace}"],
        helm_chart_path=LOCAL_HELM_CHART_DIR,
    )
    # create database roles in member clusters.
    for mcc in member_cluster_clients:
        if skip_central_cluster and mcc.cluster_name == central_cluster_name:
            continue
        create_or_replace_from_yaml(mcc.api_client, yaml_file)
    os.remove(yaml_file)
