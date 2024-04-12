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


def get_supported_version_for_image_matrix_handling(image: str) -> List[str]:
    # if we are a certifying mongodb-agent, we will need to also certify the
    # static container images which are a matrix of <agent_version>_<operator_version>
    if image == "mongodb-agent":
        # officially, we start the support with 1.25.0
        min_supported_version_operator_for_static = "1.25.0"
        last_supported_operator_versions = [
            v
            for v in get_release()["supportedImages"]["operator"]["versions"]
            if v >= min_supported_version_operator_for_static
        ]
        agent_versions_to_be_build = build_agent_gather_versions(get_release())
        agent_version_with_operator = list()
        for agent in agent_versions_to_be_build:
            for version in last_supported_operator_versions:
                agent_version_with_operator.append(agent + "_" + version)
        agent_versions_without_operator = get_release()["supportedImages"][image]["versions"]
        return sorted(list(set(agent_version_with_operator + agent_versions_without_operator)))

    return sorted(get_release()["supportedImages"][image]["versions"])
