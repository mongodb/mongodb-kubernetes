import json
import os

from scripts.release.build.build_info import load_build_info
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.release_info import convert_to_release_info_json

OPERATOR_VERSION = "1.6.0"


def test_create_release_info_json():
    expected_json = {
        "images": {
            "operator": {
                "repoURL": "quay.io/mongodb/mongodb-kubernetes",
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "tag": "1.6.0",
                "digest": "sha256:317a7f2d40807629b1df78e7ef81790bcaeb09993d88b476ec3a33ee44cbb78d",
            },
            "init-database": {
                "repoURL": "quay.io/mongodb/mongodb-kubernetes-init-database",
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "tag": "1.6.0",
                "digest": "sha256:ce10a711a6e6a31d20deecbe0ef15b5f82c2ca24d495fb832f5199ac327ee8ec",
            },
            "init-appdb": {
                "repoURL": "quay.io/mongodb/mongodb-kubernetes-init-appdb",
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "tag": "1.6.0",
                "digest": "sha256:b0b66397056636052756157628c164d480f540d919a615d057192d553a7e892c",
            },
            "init-ops-manager": {
                "repoURL": "quay.io/mongodb/mongodb-kubernetes-init-ops-manager",
                "platforms": ["linux/amd64"],
                "tag": "1.6.0",
                "digest": "sha256:62825c8edcd45e26586cce5b4062d6930847db0c58a76c168312be8cdc934707",
            },
            "database": {
                "repoURL": "quay.io/mongodb/mongodb-kubernetes-database",
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "tag": "1.6.0",
                "digest": "sha256:382248da5bdd90c8dbb0a1571b5f9ec90c10931c7e0974f4563a522963304b58",
            },
            "ops-manager": {
                "repoURL": "quay.io/mongodb/mongodb-enterprise-ops-manager-ubi",
                "platforms": ["linux/amd64"],
                "tag": "8.0.16",
                "digest": "sha256:ca4aad523f14d68fccb60256f9ce8909c66ebb5b321ee15e5abf9ac5738947f9",
            },
            "agent": {
                "repoURL": "quay.io/mongodb/mongodb-agent",
                "platforms": ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
                "tag": "108.0.16.8895-1",
                "digest": "sha256:793ae31c0d328fb3df1d3aa526f94e466cc2ed3410dd865548ce38fa3859cbaa",
            },
            "upgrade-hook": {
                "repoURL": "quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook",
                "platforms": ["linux/arm64", "linux/amd64"],
                "tag": "1.0.10",
                "digest": "sha256:f321ec1d25d6e98805b8be9321f2a926d702835136dde88d5fffe917c2df1d0a",
            },
            "readiness-probe": {
                "repoURL": "quay.io/mongodb/mongodb-kubernetes-readinessprobe",
                "platforms": ["linux/arm64", "linux/amd64"],
                "tag": "1.0.23",
                "digest": "sha256:436fc328f3887f022a4760afd03da1a7091d285baf3d627a17d80bbdaab0ee47",
            },
            "search": {
                "repoURL": "quay.io/mongodb/mongodb-search",
                "platforms": ["linux/arm64", "linux/amd64"],
                "tag": "0.55.0",
                "digest": "sha256:c1e636119aa206ff98cefed37ee4b488d75c6a5e6025dcb71f44275a8f3f546a",
            },
            "mongodb-enterprise-server": {
                "repoURL": "quay.io/mongodb/mongodb-enterprise-server",
                "platforms": ["linux/arm64", "linux/amd64"],
                "tag": "8.0.0-ubi9",
                "digest": "sha256:7a93a0276531ff9be4c90bb8fe8d104e0a9e930c29792aafe03cc6a76a9fa89c",
            },
        },
        "latestOpsManagerAgentMapping": [
            {"6": {"opsManagerVersion": "6.0.27", "agentVersion": "12.0.35.7911-1"}},
            {"7": {"opsManagerVersion": "7.0.19", "agentVersion": "107.0.19.8805-1"}},
            {"8": {"opsManagerVersion": "8.0.16", "agentVersion": "108.0.16.8895-1"}},
        ],
    }

    build_info = load_build_info(scenario=BuildScenario.RELEASE)

    # release_test.json is just a copy of our original release.json file and it's created so that we can easily
    # test release_info.py. If we directly used release.json, we will have to change the expected output `expected_json`
    # because content of release.json changes after every MCK/OM/Agent release.
    test_release_json_path = os.path.join(os.getcwd(), "scripts/release/tests/testdata/release_test.json")
    release_info_asset = convert_to_release_info_json(build_info, test_release_json_path, OPERATOR_VERSION)

    assert release_info_asset == expected_json
