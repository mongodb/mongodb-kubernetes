import json

from scripts.release.release_info import DUMMY_VERSION, create_release_info_json


def test_create_release_info_json():
    expected_json = {
        "images": {
            "operator": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes"],
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "version": DUMMY_VERSION,
            },
            "init-database": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-init-database"],
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "version": DUMMY_VERSION,
            },
            "init-appdb": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-init-appdb"],
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "version": DUMMY_VERSION,
            },
            "init-ops-manager": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-init-ops-manager"],
                "platforms": ["linux/amd64"],
                "version": DUMMY_VERSION,
            },
            "database": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-database"],
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "version": DUMMY_VERSION,
            },
            "readiness-probe": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-readinessprobe"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": DUMMY_VERSION,
            },
            "upgrade-hook": {
                "repositories": ["quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook"],
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": DUMMY_VERSION,
            },
        },
        "binaries": {
            "kubectl-mongodb": {
                "platforms": ["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"],
                "version": DUMMY_VERSION,
            }
        },
        "helm-charts": {
            "mongodb-kubernetes": {"registry": "quay.io", "repository": "mongodb/helm-charts", "version": DUMMY_VERSION}
        },
    }
    expected_release_info_json = json.dumps(expected_json, indent=2)
    release_info_json = create_release_info_json()

    assert release_info_json == expected_release_info_json
