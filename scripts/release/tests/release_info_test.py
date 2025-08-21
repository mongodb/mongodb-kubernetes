import json

from git import Repo

from scripts.release.constants import DEFAULT_CHANGELOG_PATH
from scripts.release.release_info import create_release_info_json


def test_create_release_info_json(
    git_repo: Repo, readinessprobe_version: str, operator_version_upgrade_post_start_hook_version: str
):
    git_repo.git.checkout("1.2.0")

    expected_json = {
        "images": {
            "operator": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.2.0",
            },
            "init-database": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-init-database"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.2.0",
            },
            "init-appdb": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-init-appdb"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.2.0",
            },
            "init-ops-manager": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-init-ops-manager"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.2.0",
            },
            "database": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-database"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.2.0",
            },
            "readiness-probe": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-readinessprobe"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": readinessprobe_version,
            },
            "upgrade-hook": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": operator_version_upgrade_post_start_hook_version,
            },
        },
        "binaries": {
            "kubectl-mongodb": {
                "platforms": ["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                "version": "1.2.0",
            }
        },
        "helm-charts": {"mongodb-kubernetes": {"repositories": ["quay.io/mongodb/helm-charts"], "version": "1.2.0"}},
    }
    expected_release_info_json = json.dumps(expected_json, indent=2)
    release_info_json = create_release_info_json(
        repository_path=git_repo.working_dir, changelog_sub_path=DEFAULT_CHANGELOG_PATH
    )

    assert release_info_json == expected_release_info_json
