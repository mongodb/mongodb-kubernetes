import pytest

from scripts.release.release_om_and_agents import get_latest_om_versions_from_evergreen_yaml

EVERGREEN_YAML_TEMPLATE = """
variables:
{variables}
"""


def write_evergreen_yaml(tmp_path, variables_block: str):
    (tmp_path / ".evergreen.yml").write_text(EVERGREEN_YAML_TEMPLATE.format(variables=variables_block))


def test_stable_versions_and_prerelease_only_returns_stable(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    write_evergreen_yaml(
        tmp_path,
        "  - &ops_manager_60_latest 6.0.27\n"
        "  - &ops_manager_70_latest 7.0.23\n"
        "  - &ops_manager_80_latest 8.0.23\n"
        "  - &ops_manager_90_latest 9.0.0-rc0\n",
    )

    versions = get_latest_om_versions_from_evergreen_yaml()

    assert versions == {"6.0.27", "7.0.23", "8.0.23"}


def test_stable_versions_returned_unchanged(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    write_evergreen_yaml(
        tmp_path,
        "  - &ops_manager_60_latest 6.0.27\n"
        "  - &ops_manager_70_latest 7.0.23\n"
        "  - &ops_manager_80_latest 8.0.23\n",
    )

    versions = get_latest_om_versions_from_evergreen_yaml()

    assert versions == {"6.0.27", "7.0.23", "8.0.23"}


def test_only_prereleases_raises_runtime_error(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    write_evergreen_yaml(
        tmp_path,
        "  - &ops_manager_90_latest 9.0.0-rc0\n"
        "  - &ops_manager_91_latest 9.1.0-rc1\n",
    )

    with pytest.raises(RuntimeError, match="No valid OM versions found"):
        get_latest_om_versions_from_evergreen_yaml()


def test_non_string_and_non_semver_values_are_skipped(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    write_evergreen_yaml(
        tmp_path,
        "  - &ops_manager_60_latest 6.0.27\n"
        "  - some_non_semver_string\n"
        "  - 42\n"
        "  - true\n",
    )

    versions = get_latest_om_versions_from_evergreen_yaml()

    assert versions == {"6.0.27"}
