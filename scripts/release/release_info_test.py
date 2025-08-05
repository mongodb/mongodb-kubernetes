import json

from git import Repo

from scripts.release.release_info import create_release_info_json


def test_create_release_info_json(
    git_repo: Repo, readinessprobe_version: str, operator_version_upgrade_post_start_hook_version: str
):
    git_repo.git.checkout("1.0.0")

    expected_json = {
        "images": {
            "mongodbOperator": {
                "repository": "quay.io/mongodb/mongodb-kubernetes",
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.0.0",
            },
            "initDatabase": {
                "repository": "quay.io/mongodb/mongodb-kubernetes-init-database",
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.0.0",
            },
            "initAppDb": {
                "repository": "quay.io/mongodb/mongodb-kubernetes-init-appdb",
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.0.0",
            },
            "initOpsManager": {
                "repository": "quay.io/mongodb/mongodb-kubernetes-init-ops-manager",
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.0.0",
            },
            "database": {
                "repository": "quay.io/mongodb/mongodb-kubernetes-database",
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": "1.0.0",
            },
            "readinessprobe": {
                "repository": "quay.io/mongodb/mongodb-kubernetes-readinessprobe",
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": readinessprobe_version,
            },
            "operator-version-upgrade-post-start-hook": {
                "repository": "quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook",
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": operator_version_upgrade_post_start_hook_version,
            },
        },
        "binaries": {
            "kubectl-mongodb": {
                "platforms": ["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                "version": "1.0.0",
            }
        },
        "helm-charts": {"mongodb-kubernetes": {"repository": "quay.io/mongodb/helm-charts", "version": "1.0.0"}},
    }
    expected_release_info_json = json.dumps(expected_json, indent=2)
    release_info_json = create_release_info_json(git_repo.working_dir)

    assert release_info_json == expected_release_info_json
