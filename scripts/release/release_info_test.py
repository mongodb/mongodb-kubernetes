import json

from scripts.release.release_info import (
    BinaryInfo,
    BuildInfo,
    HelmChartInfo,
    ImageInfo,
    create_release_info_json,
    load_build_info,
)
from scripts.release.version import Environment


def test_load_build_info_dev():
    build_id = "688364423f9b6c00072b3556"

    expected_build_info = BuildInfo(
        images={
            "mongodbOperator": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes",
                platforms=["linux/amd64"],
                version=build_id,
            ),
            "initDatabase": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-database",
                platforms=["linux/amd64"],
                version=build_id,
            ),
            "initAppDb": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-appdb",
                platforms=["linux/amd64"],
                version=build_id,
            ),
            "initOpsManager": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-ops-manager",
                platforms=["linux/amd64"],
                version=build_id,
            ),
            "database": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-database",
                platforms=["linux/amd64"],
                version=build_id,
            ),
            "readinessprobe": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-readinessprobe",
                platforms=["linux/amd64"],
                version=build_id,
            ),
            "operator-version-upgrade-post-start-hook": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-operator-version-upgrade-post-start-hook",
                platforms=["linux/amd64"],
                version=build_id,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/dev",
                platforms=["linux/amd64"],
                version=build_id,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/helm-charts",
                version=build_id,
            )
        },
    )

    build_info = load_build_info(Environment.DEV, build_id)

    assert build_info.__dict__() == expected_build_info.__dict__()


def test_load_build_info_staging():
    commit_sha = "05029e97"

    expected_build_info = BuildInfo(
        images={
            "mongodbOperator": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=commit_sha,
            ),
            "initDatabase": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-database-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=commit_sha,
            ),
            "initAppDb": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-appdb-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=commit_sha,
            ),
            "initOpsManager": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-ops-manager-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=commit_sha,
            ),
            "database": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-database-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=commit_sha,
            ),
            "readinessprobe": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-readinessprobe-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=commit_sha,
            ),
            "operator-version-upgrade-post-start-hook": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=commit_sha,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/staging",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                version=commit_sha,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="quay.io/mongodb/helm-charts-stg",
                version=commit_sha,
            )
        },
    )

    build_info = load_build_info(Environment.STAGING, commit_sha)

    assert build_info.__dict__() == expected_build_info.__dict__()


def test_load_build_info_prod(readinessprobe_version, operator_version_upgrade_post_start_hook_version):
    version = "1.2.3"

    expected_build_info = BuildInfo(
        images={
            "mongodbOperator": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
            ),
            "initDatabase": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-database",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
            ),
            "initAppDb": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-appdb",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
            ),
            "initOpsManager": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-ops-manager",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
            ),
            "database": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-database",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
            ),
            "readinessprobe": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-readinessprobe",
                platforms=["linux/arm64", "linux/amd64"],
                version=readinessprobe_version,
            ),
            "operator-version-upgrade-post-start-hook": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook",
                platforms=["linux/arm64", "linux/amd64"],
                version=operator_version_upgrade_post_start_hook_version,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/prod",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                version="1.2.3",
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="quay.io/mongodb/helm-charts",
                version=version,
            )
        },
    )

    build_info = load_build_info(Environment.PROD, version)

    assert build_info.__dict__() == expected_build_info.__dict__()


def test_create_release_info_json(readinessprobe_version, operator_version_upgrade_post_start_hook_version):
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
    release_info_json = create_release_info_json("1.0.0")

    assert release_info_json == expected_release_info_json
