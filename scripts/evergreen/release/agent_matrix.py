import json
from typing import Dict, List

DEFAULT_SUPPORTED_OPERATOR_VERSIONS = 3
LATEST_OPERATOR_VERSION = 1


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


def get_supported_version_for_image(image: str) -> List[str]:
    if image == "mongodb-agent":
        return build_agent_gather_versions(get_release())
    return sorted(get_release()["supportedImages"][image]["versions"])


def get_supported_operator_versions(supported_versions: int = DEFAULT_SUPPORTED_OPERATOR_VERSIONS):
    operator_versions = list(get_release()["supportedImages"]["mongodb-kubernetes"]["versions"])
    operator_versions.sort(key=lambda s: list(map(int, s.split("."))))

    if len(operator_versions) <= supported_versions:
        return operator_versions

    return operator_versions[-supported_versions:]
