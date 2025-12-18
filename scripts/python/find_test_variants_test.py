import io
import os
import sys

import pytest

from scripts.python.find_test_variants import extract_task_name_from_url, find_task_variants, main

# This test file uses our real .evergreen.yml, so it might require adjustments if we change the test structure


def test_find_task_variants():
    project_dir = os.environ.get("PROJECT_DIR", ".")
    evergreen_file = os.path.join(project_dir, ".evergreen.yml")
    result = find_task_variants(evergreen_file, "e2e_feature_controls_authentication")
    assert sorted(result) == ["e2e_mdb_kind_ubi_cloudqa", "e2e_static_mdb_kind_ubi_cloudqa"]

    result = find_task_variants(evergreen_file, "e2e_sharded_cluster")
    assert sorted(result) == [
        "e2e_mdb_kind_ubi_cloudqa",
        "e2e_multi_cluster_kind",
        "e2e_static_mdb_kind_ubi_cloudqa",
        "e2e_static_multi_cluster_kind",
    ]

    result = find_task_variants(evergreen_file, "")
    assert sorted(result) == []

    result = find_task_variants(evergreen_file, "invalid!")
    assert sorted(result) == []


def test_main_output(monkeypatch):
    project_dir = os.environ.get("PROJECT_DIR", ".")
    evergreen_file = os.path.join(project_dir, ".evergreen.yml")
    args = [
        "find_task_variants.py",
        "--evergreen-file",
        evergreen_file,
        "--task-name",
        "e2e_feature_controls_authentication",
    ]
    monkeypatch.setattr(sys, "argv", args)
    captured = io.StringIO()
    monkeypatch.setattr("sys.stdout", captured)
    main()
    output = captured.getvalue().strip().splitlines()
    assert sorted(output) == ["e2e_mdb_kind_ubi_cloudqa", "e2e_static_mdb_kind_ubi_cloudqa"]


def test_main_output_no_matches(monkeypatch):
    """
    Test that main() exits with code 1 when there are no matching variants.
    """
    project_dir = os.environ.get("PROJECT_DIR", ".")
    evergreen_file = os.path.join(project_dir, ".evergreen.yml")
    args = ["find_task_variants.py", "--evergreen-file", evergreen_file, "--task-name", "nonexistent_task_name"]
    monkeypatch.setattr(sys, "argv", args)
    captured = io.StringIO()
    monkeypatch.setattr("sys.stdout", captured)
    with pytest.raises(SystemExit) as e:
        main()
    assert e.value.code == 1
    assert captured.getvalue().strip() == ""


def test_extract_task_name():
    url = "https://spruce.mongodb.com/task/mongodb_kubernetes_e2e_custom_domain_mdb_kind_ubi_cloudqa_e2e_replica_set_patch_ca24d93d7a931f7853a679b4576674cace37bb16_6851672289288f00073de47a_25_06_17_13_01_24/logs?execution=0"
    expected = "mongodb_kubernetes_e2e_custom_domain_mdb_kind_ubi_cloudqa_e2e_replica_set_patch_ca24d93d7a931f7853a679b4576674cace37bb16_6851672289288f00073de47a_25_06_17_13_01_24"
    assert extract_task_name_from_url(url) == expected


def test_extract_task_name_invalid_url():
    invalid_url = (
        "https://spruce.mongodb.com/version/6851672289288f00073de47a/tasks?sorts=STATUS%3AASC%3BBASE_STATUS%3ADESC"
    )
    with pytest.raises(Exception):
        extract_task_name_from_url(invalid_url)


def test_extract_task_name_empty():
    with pytest.raises(Exception):
        extract_task_name_from_url("")
