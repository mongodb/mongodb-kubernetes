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
    patch_id = "688364423f9b6c00072b3556"
    os.environ["version_id"] = patch_id

    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "init-database": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-database",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "init-appdb": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-appdb",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "init-ops-manager": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-ops-manager",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "database": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-database",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "mco-tests": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-community-tests",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "meko-tests": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-tests",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "readiness-probe": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-readinessprobe",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "upgrade-hook": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-operator-version-upgrade-post-start-hook",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "agent": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            ),
            "ops-manager": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-ops-manager",
                platforms=["linux/amd64"],
                version="om-version-from-release.json",
                sign=False,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/dev",
                platforms=["linux/amd64"],
                version=patch_id,
                sign=False,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/helm-charts",
                version=patch_id,
                sign=False,
            )
        },
    )

    build_info = load_build_info(BuildScenario.PATCH, git_repo.working_dir)

    assert build_info == expected_build_info


def test_load_build_info_staging(git_repo: Repo):
    initial_commit = list(git_repo.iter_commits(reverse=True))[4]
    git_repo.git.checkout(initial_commit)
    expecter_commit_sha = initial_commit.hexsha[:8]

    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "init-database": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-database",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "init-appdb": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-appdb",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "init-ops-manager": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-ops-manager",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "database": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-database",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "mco-tests": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-community-tests",
                platforms=["linux/amd64"],
                version=expecter_commit_sha,
                sign=False,
            ),
            "meko-tests": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-tests",
                platforms=["linux/amd64"],
                version=expecter_commit_sha,
                sign=False,
            ),
            "readiness-probe": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-readinessprobe",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "upgrade-hook": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-operator-version-upgrade-post-start-hook",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "agent": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent-ubi",
                platforms=["linux/arm64", "linux/amd64"],
                version=expecter_commit_sha,
                sign=True,
            ),
            "ops-manager": ImageInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-enterprise-ops-manager",
                platforms=["linux/arm64", "linux/amd64"],
                version="om-version-from-release.json",
                sign=True,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/staging",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                version=expecter_commit_sha,
                sign=True,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/helm-charts",
                version=expecter_commit_sha,
                sign=True,
            )
        },
    )

    build_info = load_build_info(BuildScenario.STAGING, git_repo.working_dir)

    assert build_info == expected_build_info


def test_load_build_info_release(git_repo: Repo, readinessprobe_version: str,
                                 operator_version_upgrade_post_start_hook_version: str):
    version = "1.2.0"
    git_repo.git.checkout(version)

    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
                sign=True,
            ),
            "init-database": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-database",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
                sign=True,
            ),
            "init-appdb": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-appdb",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
                sign=True,
            ),
            "init-ops-manager": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-init-ops-manager",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
                sign=True,
            ),
            "database": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-database",
                platforms=["linux/arm64", "linux/amd64"],
                version=version,
                sign=True,
            ),
            "readiness-probe": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-readinessprobe",
                platforms=["linux/arm64", "linux/amd64"],
                version=readinessprobe_version,
                sign=True,
            ),
            "upgrade-hook": ImageInfo(
                repository="quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook",
                platforms=["linux/arm64", "linux/amd64"],
                version=operator_version_upgrade_post_start_hook_version,
                sign=True,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/prod",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                version=version,
                sign=True,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repository="quay.io/mongodb/helm-charts",
                version=version,
                sign=True,
            )
        },
    )

    build_info = load_build_info(BuildScenario.RELEASE, git_repo.working_dir)

    assert build_info == expected_build_info
