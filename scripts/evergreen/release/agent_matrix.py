import json
from typing import Dict, List

DEFAULT_SUPPORTED_OPERATOR_VERSIONS = 3
LATEST_OPERATOR_VERSION = 1


def get_release() -> Dict[str, str]:
    return json.load(open("release.json"))


def build_agent_gather_versions(release: Dict[str, str]):
    agent_versions = set()

    # Add cloud manager version
    cloud_manager = release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"]
    agent_versions.add(cloud_manager)

    # Add ops manager versions
    for _, om in release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"].items():
        agent_versions.add(om["agent_version"])

    return sorted(list(agent_versions))


def get_supported_version_for_image(image: str) -> List[str]:
    if image == "mongodb-agent" or image == "mongodb-agent-ubi":
        return build_agent_gather_versions(get_release())
    return sorted(get_release()["supportedImages"][image]["versions"])
