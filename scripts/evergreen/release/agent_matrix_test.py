from unittest import mock

from scripts.evergreen.release.agent_matrix import get_supported_operator_versions

empty_release = {"supportedImages": {"mongodb-kubernetes": {"versions": []}}}


@mock.patch("scripts.evergreen.release.agent_matrix.get_release", return_value=empty_release)
def test_get_supported_operator_versions_empty(_):
    supported_versions = get_supported_operator_versions()
    assert len(supported_versions) == 0


single_release = {"supportedImages": {"mongodb-kubernetes": {"versions": ["1.30.0"]}}}


@mock.patch("scripts.evergreen.release.agent_matrix.get_release", return_value=single_release)
def test_get_supported_operator_versions_single_release(_):
    supported_versions = get_supported_operator_versions()
    assert len(supported_versions) == 1
    assert supported_versions[0] == "1.30.0"


three_releases_not_ordered = {"supportedImages": {"mongodb-kubernetes": {"versions": ["1.30.0", "1.28.0", "2.0.2"]}}}


@mock.patch("scripts.evergreen.release.agent_matrix.get_release", return_value=three_releases_not_ordered)
def test_get_supported_operator_versions_three_releases_not_ordered(_):
    supported_versions = get_supported_operator_versions()
    assert len(supported_versions) == 3
    assert supported_versions == ["1.28.0", "1.30.0", "2.0.2"]


many_releases_not_ordered = {
    "supportedImages": {
        "mongodb-kubernetes": {
            "versions": [
                "1.32.0",
                "1.25.0",
                "1.26.0",
                "1.27.1",
                "1.27.0",
                "1.30.0",
                "1.300.0",
                "1.28.123",
                "0.0.1",
                "2.0.2",
            ]
        }
    }
}


@mock.patch("scripts.evergreen.release.agent_matrix.get_release", return_value=many_releases_not_ordered)
def test_get_supported_operator_versions_many_releases_not_ordered(_):
    supported_versions = get_supported_operator_versions()
    assert len(supported_versions) == 3
    assert supported_versions == ["1.32.0", "1.300.0", "2.0.2"]
