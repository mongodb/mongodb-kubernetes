import json
from typing import Dict

from _pytest.fixtures import fixture


def get_manually_upgradable_versions() -> Dict[str, str]:
    with open("build_info.json", "r") as f:
        build_info = json.load(f)

    return {
        "readinessprobe": build_info["images"]["readinessprobe"]["prod"]["version"],
        "operator_version_upgrade_post_start_hook": build_info["images"]["operator-version-upgrade-post-start-hook"][
            "prod"
        ]["version"],
    }


@fixture(scope="module")
def readinessprobe_version() -> str:
    return get_manually_upgradable_versions()["readinessprobe"]


@fixture(scope="module")
def operator_version_upgrade_post_start_hook_version() -> str:
    return get_manually_upgradable_versions()["operator_version_upgrade_post_start_hook"]
