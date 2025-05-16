#!/usr/bin/env python3

"""
Performs the update of release fields in all relevant files in the project
Note, that the script must be called from the root of the project

Usage:
    update_helm_values_files.py
"""
import json
import sys
from typing import List

from agent_matrix import get_supported_version_for_image_matrix_handling
from helm_files_handler import (
    get_value_in_yaml_file,
    set_value_in_yaml_file,
    update_all_helm_values_files,
    update_standalone_installer,
)
from packaging.version import Version

RELEASE_JSON_TO_HELM_KEY = {
    "mongodbOperator": "operator",
    "initDatabaseVersion": "initDatabase",
    "initOpsManagerVersion": "initOpsManager",
    "initAppDbVersion": "initAppDb",
    "databaseImageVersion": "database",
    "agentVersion": "agent",
}


def load_release():
    with open("release.json", "r") as fd:
        return json.load(fd)


def filterNonReleaseOut(versions: List[str]) -> List[str]:
    """Filters out all Release Candidate versions"""
    return list(filter(lambda x: "-rc" not in x, versions))


def main() -> int:
    release = load_release()

    operator_version = release["mongodbOperator"]

    for k in release:
        if k in RELEASE_JSON_TO_HELM_KEY:
            update_all_helm_values_files(RELEASE_JSON_TO_HELM_KEY[k], release[k])

    update_helm_charts(operator_version, release)
    update_standalone(operator_version)
    update_cluster_service_version(operator_version)

    return 0


def update_standalone(operator_version):
    update_standalone_installer("public/mongodb-kubernetes.yaml", operator_version),
    update_standalone_installer("public/mongodb-kubernetes-openshift.yaml", operator_version),
    update_standalone_installer("public/mongodb-kubernetes-multi-cluster.yaml", operator_version),


def update_helm_charts(operator_version, release):
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.opsManager",
        filterNonReleaseOut(release["supportedImages"]["ops-manager"]["versions"]),
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.mongodb",
        filterNonReleaseOut(release["supportedImages"]["mongodb-enterprise-server"]["versions"]),
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.agent",
        filterNonReleaseOut(get_supported_version_for_image_matrix_handling("mongodb-agent")),
    )
    set_value_in_yaml_file("helm_chart/values-openshift.yaml", "operator.version", operator_version)
    set_value_in_yaml_file("helm_chart/values.yaml", "operator.version", operator_version)
    set_value_in_yaml_file("helm_chart/Chart.yaml", "version", operator_version)

    set_value_in_yaml_file(
        "helm_chart/values.yaml", "search.community.version", release["search"]["community"]["version"]
    )


def update_cluster_service_version(operator_version):
    container_image_value = get_value_in_yaml_file(
        "config/manifests/bases/mongodb-kubernetes.clusterserviceversion.yaml",
        "metadata.annotations.containerImage",
    )

    image_parts = container_image_value.split(":")
    old_operator_version = image_parts[-1]
    image_repo = ":".join(image_parts[:-1])

    if old_operator_version != operator_version:
        set_value_in_yaml_file(
            "config/manifests/bases/mongodb-kubernetes.clusterserviceversion.yaml",
            "spec.replaces",
            f"mongodb-kubernetes.v{old_operator_version}",
            preserve_quotes=True,
        )

    set_value_in_yaml_file(
        "config/manifests/bases/mongodb-kubernetes.clusterserviceversion.yaml",
        "metadata.annotations.containerImage",
        f"{image_repo}:{operator_version}",
        preserve_quotes=True,
    )


if __name__ == "__main__":
    sys.exit(main())
