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

from agent_matrix import get_supported_version_for_image
from git import Repo
from helm_files_handler import (
    get_value_in_yaml_file,
    set_value_in_yaml_file,
    update_all_helm_values_files,
    update_community_agent_image_in_file,
    update_community_agent_image_in_go_file,
)

from scripts.release.constants import DEFAULT_RELEASE_INITIAL_VERSION, DEFAULT_REPOSITORY_PATH
from scripts.release.version import find_previous_version

RELEASE_JSON_TO_HELM_KEY = {
    "mongodbOperator": "operator",
    "initDatabaseVersion": "initDatabase",
    "initOpsManagerVersion": "initOpsManager",
    "databaseImageVersion": "database",
    "agentVersion": "agent",
    "readinessProbeVersion": "readinessProbe",
    "versionUpgradeHookVersion": "versionUpgradeHook",
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

    repo = Repo(DEFAULT_REPOSITORY_PATH)
    old_operator_version = find_previous_version(
        repo=repo, initial_commit_sha=None, initial_version=DEFAULT_RELEASE_INITIAL_VERSION
    ).name

    for k in release:
        if k in RELEASE_JSON_TO_HELM_KEY:
            update_all_helm_values_files(RELEASE_JSON_TO_HELM_KEY[k], release[k])

    agent_version = get_latest_community_agent_version(release)

    update_helm_charts(operator_version, release, agent_version)
    update_community_manifests(agent_version)
    update_cluster_service_version(operator_version, old_operator_version)

    return 0


def get_latest_community_agent_version(release) -> str:
    """Returns the agent version for the highest OM major from latestOpsManagerAgentMapping."""
    latest_mapping = release["latestOpsManagerAgentMapping"]
    latest = max(latest_mapping, key=lambda x: int(list(x.keys())[0]))
    return list(latest.values())[0]["agentVersion"]


def update_community_manifests(agent_version: str):
    # config/manager/manager.yaml is regenerated from the helm template by the generate-standalone-yaml
    # hook; updating it here would conflict (ruamel.yaml vs helm produce different byte sequences).
    update_community_agent_image_in_file(
        "mongodb-community-operator/deploy/openshift/operator_openshift.yaml", agent_version
    )
    update_community_agent_image_in_go_file("mongodb-community-operator/test/e2e/setup/test_config.go", agent_version)


def update_helm_charts(operator_version, release, agent_version):
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
        filterNonReleaseOut(get_supported_version_for_image("mongodb-agent")),
    )
    set_value_in_yaml_file("helm_chart/values-openshift.yaml", "operator.version", operator_version)
    set_value_in_yaml_file("helm_chart/values.yaml", "operator.version", operator_version)
    set_value_in_yaml_file("helm_chart/Chart.yaml", "version", operator_version)

    set_value_in_yaml_file("helm_chart/values.yaml", "search.version", release["search"]["version"])
    set_value_in_yaml_file("helm_chart/values.yaml", "community.agent.version", agent_version)


def update_cluster_service_version(operator_version: str, old_operator_version: str):
    container_image_value = get_value_in_yaml_file(
        "config/manifests/bases/mongodb-kubernetes.clusterserviceversion.yaml",
        "metadata.annotations.containerImage",
    )

    image_parts = container_image_value.split(":")
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
