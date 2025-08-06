import json
from typing import Dict

from _pytest.fixtures import fixture


def get_manually_upgradable_versions() -> Dict[str, str]:
    with open("build_info.json", "r") as f:
        build_info = json.load(f)

    return {
        "readiness-probe": build_info["images"]["readiness-probe"]["release"]["version"],
        "upgrade-hook": build_info["images"]["upgrade-hook"]["release"]["version"],
    }


@fixture(scope="module")
def readinessprobe_version() -> str:
    return get_manually_upgradable_versions()["readiness-probe"]


@fixture(scope="module")
def operator_version_upgrade_post_start_hook_version() -> str:
    return get_manually_upgradable_versions()["upgrade-hook"]
