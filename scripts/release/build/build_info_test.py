import os

from scripts.release.build.build_info import (
    BinaryInfo,
    BuildInfo,
    HelmChartInfo,
    ImageInfo,
    load_build_info,
)
from git import Repo
from scripts.release.build.build_scenario import BuildScenario


def test_load_build_info_patch(git_repo: Repo):
    build_id = "688364423f9b6c00072b3556"
    os.environ["BUILD_ID"] = build_id

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

    build_info = load_build_info(BuildScenario.PATCH, git_repo.working_dir)

    assert build_info.__dict__() == expected_build_info.__dict__()


def test_load_build_info_staging(git_repo: Repo):
    initial_commit = list(git_repo.iter_commits(reverse=True))[4]
    git_repo.git.checkout(initial_commit)
    expecter_commit_sha = initial_commit.hexsha[:8]

    expected_build_info = BuildInfo(
        images={
            "mongodbOperator": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
            ),
            "initDatabase": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-database-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
            ),
            "initAppDb": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-appdb-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
            ),
            "initOpsManager": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-ops-manager-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
            ),
            "database": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-database-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
            ),
            "readinessprobe": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-readinessprobe-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
            ),
            "operator-version-upgrade-post-start-hook": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook-stg",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/staging",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                version=expecter_commit_sha,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="quay.io/mongodb/helm-charts-stg",
                version=expecter_commit_sha,
            )
        },
    )

    build_info = load_build_info(BuildScenario.STAGING, git_repo.working_dir)

    assert build_info.__dict__() == expected_build_info.__dict__()


def test_load_build_info_release(git_repo: Repo, readinessprobe_version: str,
                                 operator_version_upgrade_post_start_hook_version: str):
    version = "1.2.0"
    git_repo.git.checkout(version)

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
                version=version,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="quay.io/mongodb/helm-charts",
                version=version,
            )
        },
    )

    build_info = load_build_info(BuildScenario.RELEASE, git_repo.working_dir)

    assert build_info.__dict__() == expected_build_info.__dict__()
