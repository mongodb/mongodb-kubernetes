from scripts.release.build.build_info import (
    BinaryInfo,
    BuildInfo,
    HelmChartInfo,
    ImageInfo,
    load_build_info,
)
from scripts.release.build.build_scenario import BuildScenario


# TODO: Target testdata (test build_info.json file) and not the production build_info.json file from codebase
def test_load_build_info_development():
    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "operator-race": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "init-database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-database"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
            ),
            "init-appdb": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-appdb"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
            ),
            "init-ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-ops-manager"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
            ),
            "database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-database"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
            ),
            "mco-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-community-tests"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-community-tests/Dockerfile",
            ),
            "meko-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-tests"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
            ),
            "meko-tests-arm64": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-tests"],
                platforms=["linux/arm64"],
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
                architecture_suffix=True,
            ),
            "readiness-probe": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
            ),
            "upgrade-hook": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-operator-version-upgrade-post-start-hook"
                ],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
            ),
            "agent": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-agent/Dockerfile",
                skip_if_exists=True,
            ),
            "ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-ops-manager-ubi"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
                skip_if_exists=True,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/dev",
                platforms=["linux/amd64"],
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/helm-charts"],
            )
        },
    )

    build_info = load_build_info(BuildScenario.DEVELOPMENT)

    assert build_info == expected_build_info


def test_load_build_info_patch():
    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "operator-race": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
            ),
            "init-database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-database"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
            ),
            "init-appdb": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-appdb"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
            ),
            "init-ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-ops-manager"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
            ),
            "database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-database"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
            ),
            "mco-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-community-tests"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-community-tests/Dockerfile",
            ),
            "meko-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-tests"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
            ),
            "meko-tests-arm64": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-tests"],
                platforms=["linux/arm64"],
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
                architecture_suffix=True,
            ),
            "readiness-probe": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
            ),
            "upgrade-hook": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-operator-version-upgrade-post-start-hook"
                ],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
            ),
            "agent": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-agent/Dockerfile",
                skip_if_exists=True,
            ),
            "ops-manager": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-ops-manager-ubi"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
                skip_if_exists=True,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/dev",
                platforms=["linux/amd64"],
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/helm-charts"],
            )
        },
    )

    build_info = load_build_info(BuildScenario.PATCH)

    assert build_info == expected_build_info


def test_load_build_info_staging():
    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
                sign=True,
            ),
            "operator-race": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
                sign=True,
            ),
            "init-database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "init-appdb": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-appdb"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "init-ops-manager": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-ops-manager"
                ],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "database": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "mco-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-community-tests"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-community-tests/Dockerfile",
            ),
            "meko-tests": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-tests"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
            ),
            "meko-tests-arm64": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-tests"],
                platforms=["linux/arm64"],
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
                architecture_suffix=True,
            ),
            "readiness-probe": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/arm64", "linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "upgrade-hook": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-operator-version-upgrade-post-start-hook"
                ],
                platforms=["linux/arm64", "linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
                latest_tag=True,
                sign=True,
            ),
            "agent": ImageInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-agent/Dockerfile",
                sign=True,
                skip_if_exists=True,
            ),
            "ops-manager": ImageInfo(
                repositories=[
                    "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-enterprise-ops-manager-ubi"
                ],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
                sign=True,
                skip_if_exists=True,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/staging",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                sign=True,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repositories=["268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/helm-charts"],
                sign=True,
            )
        },
    )

    build_info = load_build_info(BuildScenario.STAGING)

    assert build_info == expected_build_info


def test_load_build_info_release():
    expected_build_info = BuildInfo(
        images={
            "operator": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "init-database": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-init-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-init-database/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "init-appdb": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-init-appdb"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "init-ops-manager": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-init-ops-manager"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "database": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-database"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "meko-tests": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-tests"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
                skip_if_exists=True,
            ),
            "readiness-probe": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-readinessprobe"],
                platforms=["linux/arm64", "linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-readinessprobe/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "upgrade-hook": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook"],
                platforms=["linux/arm64", "linux/amd64"],
                dockerfile_path="docker/mongodb-kubernetes-upgrade-hook/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "agent": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-agent-ubi", "quay.io/mongodb/mongodb-agent"],
                platforms=["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                dockerfile_path="docker/mongodb-agent/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
            "ops-manager": ImageInfo(
                repositories=["quay.io/mongodb/mongodb-enterprise-ops-manager-ubi"],
                platforms=["linux/amd64"],
                dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
                skip_if_exists=True,
                olm_tag=True,
                sign=True,
            ),
        },
        binaries={
            "kubectl-mongodb": BinaryInfo(
                s3_store="s3://kubectl-mongodb/prod",
                platforms=["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                sign=True,
            )
        },
        helm_charts={
            "mongodb-kubernetes": HelmChartInfo(
                repositories=["quay.io/mongodb/helm-charts"],
                sign=True,
            )
        },
    )

    build_info = load_build_info(BuildScenario.RELEASE)

    assert build_info == expected_build_info
