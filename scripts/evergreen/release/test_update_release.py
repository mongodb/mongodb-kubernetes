from update_release import trim_ops_manager_mapping, trim_supported_image_versions


def test_trim_ops_manager_mapping():
    mock_release = {
        "supportedImages": {
            "mongodb-agent": {
                "opsManagerMapping": {
                    "ops_manager": {
                        "1.0.0": {"tools_version": "100.0.0"},
                        "1.5.0": {"tools_version": "100.0.5"},
                        "2.0.0": {"tools_version": "100.1.0"},
                        "2.5.0": {"tools_version": "100.1.5"},
                        "3.0.0": {"tools_version": "100.2.0"},
                    }
                }
            }
        }
    }

    # Expected result: Latest 3 versions per major version (1.x, 2.x, 3.x)
    # Since we have fewer than 3 versions for each major, all should be kept
    expected_mapping = {
        "1.0.0": {"tools_version": "100.0.0"},
        "1.5.0": {"tools_version": "100.0.5"},
        "2.0.0": {"tools_version": "100.1.0"},
        "2.5.0": {"tools_version": "100.1.5"},
        "3.0.0": {"tools_version": "100.2.0"},
    }

    trim_ops_manager_mapping(mock_release)

    ops_manager_mapping = mock_release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]

    assert len(ops_manager_mapping) == 5
    assert ops_manager_mapping == expected_mapping

    # Test with multiple versions in the same major version
    complex_release = {
        "supportedImages": {
            "mongodb-agent": {
                "opsManagerMapping": {
                    "ops_manager": {
                        "1.0.0": {"tools_version": "100.0.0"},
                        "1.5.0": {"tools_version": "100.0.5"},
                        "1.9.2": {"tools_version": "100.0.9"},
                        "1.10.0": {"tools_version": "100.0.10"},
                        "2.0.0": {"tools_version": "100.1.0"},
                        "2.5.0": {"tools_version": "100.1.5"},
                        "4.0.0": {"tools_version": "100.3.0"},
                    }
                }
            }
        }
    }

    # The function keeps the latest 3 versions per major version using semver
    # For major version 1, keep: 1.10.0, 1.9.2, 1.5.0
    # For major version 2, keep: 2.5.0, 2.0.0
    # For major version 4, keep: 4.0.0
    expected_complex_mapping = {
        "1.10.0": {"tools_version": "100.0.10"},
        "1.9.2": {"tools_version": "100.0.9"},
        "1.5.0": {"tools_version": "100.0.5"},
        "2.5.0": {"tools_version": "100.1.5"},
        "2.0.0": {"tools_version": "100.1.0"},
        "4.0.0": {"tools_version": "100.3.0"},
    }

    trim_ops_manager_mapping(complex_release)
    assert (
        complex_release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]
        == expected_complex_mapping
    )

    # Test with fewer than 3 major versions (should keep all latest per major)
    small_release = {
        "supportedImages": {
            "mongodb-agent": {
                "opsManagerMapping": {
                    "ops_manager": {
                        "1.0.0": {"tools_version": "100.0.0"},
                        "1.2.0": {"tools_version": "100.0.2"},
                        "2.0.0": {"tools_version": "100.1.0"},
                    }
                }
            }
        }
    }

    expected_small_mapping = {
        "1.2.0": {"tools_version": "100.0.2"},
        "1.0.0": {"tools_version": "100.0.0"},
        "2.0.0": {"tools_version": "100.1.0"},
    }

    trim_ops_manager_mapping(small_release)
    assert (
        small_release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"] == expected_small_mapping
    )


def test_trim_ops_manager_mapping_multiple_versions_per_major():
    """Test that only the latest 3 versions per major version are kept."""
    mock_release = {
        "supportedImages": {
            "mongodb-agent": {
                "opsManagerMapping": {
                    "ops_manager": {
                        # Five versions for major version 1
                        "1.1.0": {"tools_version": "100.1.0"},
                        "1.2.0": {"tools_version": "100.1.1"},
                        "1.3.0": {"tools_version": "100.1.2"},
                        "1.4.0": {"tools_version": "100.1.3"},
                        "1.5.0": {"tools_version": "100.1.4"},
                        # Five versions for major version 2
                        "2.1.0": {"tools_version": "100.2.0"},
                        "2.2.0": {"tools_version": "100.2.1"},
                        "2.3.0": {"tools_version": "100.2.2"},
                        "2.4.0": {"tools_version": "100.2.3"},
                        "2.5.0": {"tools_version": "100.2.4"},
                    }
                }
            }
        }
    }

    # Expected result: Keep the 3 latest versions for each major version
    expected_mapping = {
        "1.5.0": {"tools_version": "100.1.4"},
        "1.4.0": {"tools_version": "100.1.3"},
        "1.3.0": {"tools_version": "100.1.2"},
        "2.5.0": {"tools_version": "100.2.4"},
        "2.4.0": {"tools_version": "100.2.3"},
        "2.3.0": {"tools_version": "100.2.2"},
    }

    trim_ops_manager_mapping(mock_release)
    assert mock_release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"] == expected_mapping


def test_trim_ops_manager_versions():
    """Test that only the latest 3 versions per major version are kept in ops-manager versions."""
    mock_release = {
        "supportedImages": {
            "ops-manager": {
                "versions": [
                    # Five versions for major version 6
                    "6.5.0",
                    "6.4.0",
                    "6.3.0",
                    "6.2.0",
                    "6.1.0",
                    # Five versions for major version 7
                    "7.5.0",
                    "7.4.0",
                    "7.3.0",
                    "7.2.0",
                    "7.1.0",
                ]
            }
        }
    }

    # Expected result: Keep the 3 latest versions for each major version
    expected_versions = [
        "7.5.0",
        "7.4.0",
        "7.3.0",  # Latest 3 from major version 7
        "7.0.12",
        "6.5.0",
        "6.4.0",
        "6.3.0",  # Latest 3 from major version 6
    ]

    trim_supported_image_versions(mock_release, ["ops-manager"])
    assert set(mock_release["supportedImages"]["ops-manager"]["versions"]) == set(expected_versions)
    assert len(mock_release["supportedImages"]["ops-manager"]["versions"]) == 7


def test_trim_ops_manager_versions_semver_sorting():
    """Test semantic version sorting in ops-manager versions."""
    mock_release = {
        "supportedImages": {
            "ops-manager": {
                "versions": [
                    # Major version 6 with version that would sort incorrectly with string sorting
                    "6.1.0",
                    "6.2.0",
                    "6.3.0",
                    "6.10.0",
                    "6.11.0",
                    # Major version 7 with version that would sort incorrectly with string sorting
                    "7.1.0",
                    "7.10.0",
                    "7.2.0",
                ]
            }
        }
    }

    # Expected result with correct semver sorting:
    expected_versions = [
        "6.3.0",  # Latest 3 from major version 6 (semver order)
        "6.10.0",
        "6.11.0",
        "7.0.12",
        "7.1.0",  # Latest 3 from major version 7 (semver order)
        "7.2.0",
        "7.10.0",
    ]

    trim_supported_image_versions(mock_release, ["ops-manager"])
    assert mock_release["supportedImages"]["ops-manager"]["versions"] == expected_versions


def test_trim_ops_manager_versions_fewer_than_three():
    """Test when there are fewer than 3 versions for a major version."""
    mock_release = {
        "supportedImages": {
            "ops-manager": {
                "versions": [
                    # Two versions for major version 5
                    "5.2.0",
                    "5.1.0",
                    # One version for major version 6
                    "6.0.0",
                ]
            }
        }
    }

    # Expected result: Keep all versions since each major has fewer than 3
    expected_versions = ["5.1.0", "5.2.0", "6.0.0", "7.0.12"]

    trim_supported_image_versions(mock_release, ["ops-manager"])

    # Verify the versions are sorted in descending order by semver
    assert mock_release["supportedImages"]["ops-manager"]["versions"] == expected_versions


def test_trim_ops_manager_versions_missing_keys():
    """Test that the function handles missing keys gracefully."""
    mock_release = {
        "supportedImages": {
            # No ops-manager key
        }
    }

    # Should not raise an exception
    trim_supported_image_versions(mock_release, ["ops-manager"])
