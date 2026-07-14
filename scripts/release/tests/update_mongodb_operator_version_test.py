import json
from unittest.mock import patch

import pytest

SAMPLE_RELEASE_JSON = {
    "mongodbToolsBundle": {"ubi": "mongodb-database-tools-rhel88-x86_64-100.15.0.tgz"},
    "mongodbOperator": "1.8.0",
    "initDatabaseVersion": "1.8.0",
    "initOpsManagerVersion": "1.8.0",
    "databaseImageVersion": "1.8.0",
    "agentVersion": "108.0.12.8846-1",
    "openshift": {"minimumSupportedVersion": "4.6"},
}


def _write_release_json(tmp_path, data):
    release_path = tmp_path / "release.json"
    with open(release_path, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")
    return release_path


def test_round_trip_overwrites_mongodb_operator_and_preserves_rest(tmp_path, monkeypatch):
    release_path = _write_release_json(tmp_path, SAMPLE_RELEASE_JSON)
    monkeypatch.chdir(tmp_path)

    with (
        patch("scripts.release.update_mongodb_operator_version.Repo"),
        patch(
            "scripts.release.update_mongodb_operator_version.calculate_next_version",
            return_value="1.9.0",
        ),
    ):
        from scripts.release import update_mongodb_operator_version

        update_mongodb_operator_version.main()

    with open(release_path) as f:
        result = json.load(f)

    assert result["mongodbOperator"] == "1.9.0"
    expected = dict(SAMPLE_RELEASE_JSON)
    expected["mongodbOperator"] = "1.9.0"
    assert result == expected
    assert list(result.keys()) == list(SAMPLE_RELEASE_JSON.keys())

    raw = release_path.read_text()
    assert raw.endswith("\n")
    assert not raw.endswith("\n\n")
    assert '  "mongodbOperator": "1.9.0"' in raw


def test_no_op_when_value_matches(tmp_path, monkeypatch, capsys):
    release_path = _write_release_json(tmp_path, SAMPLE_RELEASE_JSON)
    original_bytes = release_path.read_bytes()
    monkeypatch.chdir(tmp_path)

    with (
        patch("scripts.release.update_mongodb_operator_version.Repo"),
        patch(
            "scripts.release.update_mongodb_operator_version.calculate_next_version",
            return_value=SAMPLE_RELEASE_JSON["mongodbOperator"],
        ),
    ):
        from scripts.release import update_mongodb_operator_version

        update_mongodb_operator_version.main()

    assert release_path.read_bytes() == original_bytes
    captured = capsys.readouterr()
    assert "(unchanged)" in captured.out


def test_failure_propagates_and_release_json_unchanged(tmp_path, monkeypatch):
    release_path = _write_release_json(tmp_path, SAMPLE_RELEASE_JSON)
    original_bytes = release_path.read_bytes()
    monkeypatch.chdir(tmp_path)

    with (
        patch("scripts.release.update_mongodb_operator_version.Repo"),
        patch(
            "scripts.release.update_mongodb_operator_version.calculate_next_version",
            side_effect=RuntimeError("simulated failure"),
        ),
    ):
        from scripts.release import update_mongodb_operator_version

        with pytest.raises(RuntimeError, match="simulated failure"):
            update_mongodb_operator_version.main()

    assert release_path.read_bytes() == original_bytes
