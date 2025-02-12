import json
from typing import Dict, List


def get_release() -> Dict[str, str]:
    return json.load(open("release.json"))


def build_agent_gather_versions(release: Dict[str, str]):
    # This is a list of a tuples - agent version and corresponding tools version
    agent_versions_to_be_build = list()
    agent_versions_to_be_build.append(
        release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"],
    )
    for _, om in release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"].items():
        agent_versions_to_be_build.append(om["agent_version"])
    return agent_versions_to_be_build


def get_supported_version_for_image_matrix_handling(image: str, latest_operator_only: bool = False) -> List[str]:
    # if we are a certifying mongodb-agent, we will need to also certify the
    # static container images which are a matrix of <agent_version>_<operator_version>
    if image == "mongodb-agent":
        # officially, we start the support with 1.25.0, but we only support the last three versions
        last_supported_operator_versions = get_supported_operator_versions()
        if latest_operator_only:
            last_supported_operator_versions = [last_supported_operator_versions[-1]]
        agent_version_with_static_support_without_operator_suffix = build_agent_gather_versions(get_release())
        agent_version_with_static_support_with_operator_suffix = list()
        for agent in agent_version_with_static_support_without_operator_suffix:
            for version in last_supported_operator_versions:
                agent_version_with_static_support_with_operator_suffix.append(agent + "_" + version)
        agent_versions_no_static_support = get_release()["supportedImages"][image]["versions"]
        agents = sorted(
            list(
                set(
                    agent_version_with_static_support_with_operator_suffix
                    + agent_version_with_static_support_without_operator_suffix
                    + list(agent_versions_no_static_support)
                )
            )
        )
        return agents

    return sorted(get_release()["supportedImages"][image]["versions"])


def get_supported_operator_versions():
    min_supported_version_operator_for_static = "1.29.0"
    last_supported_operator_versions = [
        v
        for v in get_release()["supportedImages"]["operator"]["versions"]
        if v >= min_supported_version_operator_for_static
    ]

    return sorted(last_supported_operator_versions)
