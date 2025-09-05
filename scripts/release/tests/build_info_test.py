import os

from git import Repo

from scripts.release.build.build_info import (
    BinaryInfo,
    BuildInfo,
    HelmChartInfo,
    ImageInfo,
    load_build_info,
)
from scripts.release.build.build_scenario import BuildScenario


def test_load_build_info_development(git_repo: Repo):
    version = "latest"

    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "operator-race": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "init-database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-database"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
            ),
            "init-appdb": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-appdb"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
            ),
            "init-ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-ops-manager"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
            ),
            "database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-database"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
            ),
            "mco-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-community-tests"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-community-tests/Dockerfile",
            ),
            "meko-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-tests"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
            ),
            "readiness-probe": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
            ),
            "upgrade-hook": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-operator-version-upgrade-post-start-hook"
                ],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
            ),
            "agent": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-agent/Dockerfile",
            ),
            "ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-ops-manager-ubi"],
                platforms=["linux/amd64"],
                version="om-version-from-release.json",
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/dev",
                platforms=["linux/amd64"],
                version=version,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/helm-charts"],
                version=version,
            )
        },
    )

    build_info = load_build_info(BuildScenario.DEVELOPMENT, git_repo.working_dir)

    assert build_info == expected_build_info


def test_load_build_info_patch(git_repo: Repo):
    patch_id = "688364423f9b6c00072b3556"
    os.environ["version_id"] = patch_id

    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "operator-race": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "init-database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-database"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
            ),
            "init-appdb": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-appdb"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
            ),
            "init-ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-ops-manager"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
            ),
            "database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-database"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
            ),
            "mco-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-community-tests"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-community-tests/Dockerfile",
            ),
            "meko-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-tests"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
            ),
            "readiness-probe": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
            ),
            "upgrade-hook": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-operator-version-upgrade-post-start-hook"
                ],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
            ),
            "agent": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent"],
                platforms=["linux/amd64"],
                version=patch_id,
                dockerfile_path="docker/mongodb-agent/Dockerfile",
            ),
            "ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-ops-manager-ubi"],
                platforms=["linux/amd64"],
                version="om-version-from-release.json",
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/dev",
                platforms=["linux/amd64"],
                version=patch_id,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/helm-charts"],
                version=patch_id,
            )
        },
    )

    build_info = load_build_info(BuildScenario.PATCH, git_repo.working_dir)

    assert build_info == expected_build_info


def test_load_build_info_staging(git_repo: Repo):
    initial_commit = list(git_repo.iter_commits(reverse=True))[4]
    git_repo.git.checkout(initial_commit)
    expected_commit_sha = initial_commit.hexsha[:8]

    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
                sign=True,
            ),
            "operator-race": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
                sign=True,
            ),
            "init-database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "init-appdb": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-appdb"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "init-ops-manager": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-ops-manager"
                ],
                platforms=["linux/amd64"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "mco-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-community-tests"],
                platforms=["linux/amd64"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-community-tests/Dockerfile",
            ),
            "meko-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-tests"],
                platforms=["linux/arm64", "linux/amd64"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
            ),
            "readiness-probe": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/arm64", "linux/amd64"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "upgrade-hook": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-operator-version-upgrade-post-start-hook"
                ],
                platforms=["linux/arm64", "linux/amd64"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "agent": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=expected_commit_sha,
                dockerfile_path="docker/mongodb-agent/Dockerfile",
                sign=True,
            ),
            "ops-manager": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-enterprise-ops-manager-ubi"
                ],
                platforms=["linux/amd64"],
                version="om-version-from-release.json",
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
                sign=True,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/staging",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                version=expected_commit_sha,
                sign=True,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/helm-charts"],
                version=expected_commit_sha,
                sign=True,
            )
        },
    )

    build_info = load_build_info(BuildScenario.STAGING, git_repo.working_dir)

    assert build_info == expected_build_info


def test_load_build_info_release(
    git_repo: Repo, readinessprobe_version: str, operator_version_upgrade_post_start_hook_version: str
):
    version = "1.2.0"
    git_repo.git.checkout(version)

    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
            "init-database": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-init-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
            "init-appdb": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-init-appdb"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
            "init-ops-manager": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-init-ops-manager"],
                platforms=["linux/amd64"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
            "database": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=version,
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
            "readiness-probe": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/arm64", "linux/amd64"],
                version=readinessprobe_version,
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
            "upgrade-hook": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook"],
                platforms=["linux/arm64", "linux/amd64"],
                version=operator_version_upgrade_post_start_hook_version,
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
                olm_tag=True,
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
                repositories=["quay.io/mongodb/helm-charts"],
                version=version,
                sign=True,
            )
        },
    )

    build_info = load_build_info(BuildScenario.RELEASE, git_repo.working_dir)

    assert build_info == expected_build_info


def test_load_build_info_manual_release(git_repo: Repo):
    version = "1.2.0"
    git_repo.git.checkout(version)

    expected_build_info = BuildInfo(
        images={
            "agent": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-agent-ubi", "quay.io/mongodb/mongodb-agent"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                version=None,  # Version is None for manual_release scenario
                dockerfile_path="docker/mongodb-agent/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
            "ops-manager": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-enterprise-ops-manager-ubi"],
                platforms=["linux/amd64"],
                version=None,  # Version is None for manual_release scenario
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
                olm_tag=True,
                sign=True,
            ),
        },
        binaries={},
        helm_charts={},
    )

    build_info = load_build_info(BuildScenario.MANUAL_RELEASE, git_repo.working_dir)

    assert build_info == expected_build_info
