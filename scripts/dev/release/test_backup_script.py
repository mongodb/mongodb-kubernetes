#!/usr/bin/env python3

import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

import yaml
from backup_csv_images import (
    backup_image_process,
    extract_images_from_csv,
    filter_digest_pinned_images,
    generate_backup_tag,
    parse_csv_file,
    parse_image_url,
    run_command,
)


def get_test_csv():
    """Create a test ClusterServiceVersion with digest-pinned images."""
    test_csv = {
        "apiVersion": "operators.coreos.com/v1alpha1",
        "kind": "ClusterServiceVersion",
        "metadata": {
            "name": "test-operator.v1.0.0",
            "annotations": {"containerImage": "quay.io/test/operator@sha256:abc123def456"},
        },
        "spec": {
            "install": {
                "spec": {
                    "deployments": [
                        {
                            "spec": {
                                "template": {
                                    "spec": {
                                        "containers": [
                                            {
                                                "name": "operator",
                                                "image": "quay.io/test/operator@sha256:abc123def456",
                                                "env": [
                                                    {
                                                        "name": "RELATED_IMAGE_DATABASE",
                                                        "value": "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789",
                                                    },
                                                    {
                                                        "name": "RELATED_IMAGE_AGENT",
                                                        "value": "quay.io/mongodb/mongodb-agent@sha256:ghi789jkl012",
                                                    },
                                                    {"name": "REGULAR_ENV_VAR", "value": "not-an-image"},
                                                ],
                                            }
                                        ]
                                    }
                                }
                            }
                        }
                    ]
                }
            },
            "relatedImages": [
                {"name": "database-image", "image": "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789"},
                {"name": "agent-image", "image": "quay.io/mongodb/mongodb-agent@sha256:ghi789jkl012"},
                {"name": "ops-manager-image", "image": "quay.io/mongodb/ops-manager@sha256:jkl012mno345"},
            ],
        },
    }
    return test_csv


def test_parse_image_url():
    """Test URL parsing for digest-pinned images."""
    test_cases = [
        ("quay.io/mongodb/operator@sha256:abc123", ("quay.io", "mongodb/operator", "sha256:abc123")),
        ("quay.io/mongodb/mongodb-agent@sha256:def456", ("quay.io", "mongodb/mongodb-agent", "sha256:def456")),
        ("docker.io/library/nginx@sha256:123456", ("docker.io", "library/nginx", "sha256:123456")),
    ]

    for image_url, expected in test_cases:
        result = parse_image_url(image_url)
        print(f"  {image_url} -> {result}")
        assert result == expected, f"Expected {expected}, got {result}"

    # Test invalid formats
    invalid_cases = [
        "registry.io/repo/image:v1.0.0",  # Tag instead of digest
        "simple-image:latest",  # No registry, tag instead of digest
        "simple-image",  # No registry, no tag/digest
        "quay.io/mongodb/operator",  # No digest
    ]

    for image_url in invalid_cases:
        try:
            parse_image_url(image_url)
            assert False, f"Expected ValueError for {image_url}, but no exception was raised"
        except ValueError as e:
            print(f"  Correctly rejected invalid format: {image_url}")
            continue


def test_backup_tag_generation():
    """Test backup tag generation."""
    test_cases = [
        ("quay.io/mongodb/operator@sha256:abc123", "1.31.0", "1.0.0"),
        ("quay.io/mongodb/mongodb-agent@sha256:def456", "107.0.12.8669-1", "1.2.0"),
        ("quay.io/mongodb/mongodb-agent@sha256:ghi789", "12.0.33.7866_1.1.1.0", "1.2.0"),
        ("quay.io/mongodb/mongodb-enterprise-server@sha256:jkl012", "4.4.15-ubi8", "1.0.0"),
    ]

    for image, original_tag, mck_version in test_cases:
        backup_tag = generate_backup_tag(image, original_tag, mck_version)
        print(f"  {image} (tag: {original_tag}, version: {mck_version}")
        print(f"    -> {backup_tag}")

        # Extract repo name from original image
        repo_name = image.split("@")[0].split("/")[-1]

        expected = f"quay.io/mongodb/{repo_name}:{original_tag}_openshift_{mck_version}"

        assert backup_tag == expected, f"Expected {expected}, got {backup_tag}"


def test_csv_parsing():
    """Test CSV parsing and image extraction."""

    # Create test CSV
    test_csv = get_test_csv()

    # Write to temporary file
    with tempfile.NamedTemporaryFile(mode="w", suffix=".yaml", delete=False) as f:
        yaml.dump(test_csv, f)
        temp_path = Path(f.name)

    try:
        # Parse the CSV
        csv_data = parse_csv_file(temp_path)
        assert csv_data is not None

        # Extract images (should only get images from relatedImages section)
        all_images = extract_images_from_csv(csv_data)

        # Should find 3 unique images from relatedImages section
        expected_image_urls = {
            "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789",
            "quay.io/mongodb/mongodb-agent@sha256:ghi789jkl012",
            "quay.io/mongodb/ops-manager@sha256:jkl012mno345",
        }
        assert (
            set(all_images.keys()) == expected_image_urls
        ), f"Expected {expected_image_urls}, got {set(all_images.keys())}"

        # Filter digest-pinned images
        digest_images = filter_digest_pinned_images(all_images)
        print(f"  Found {len(digest_images)} digest-pinned images:")

        # Expected backup tags based on the new format
        # Format: quay.io/mongodb/{repo_name}:{original_tag}_openshift_{version}
        expected_tags = {
            "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789": ("mongodb-enterprise-server", "1.0.0"),
            "quay.io/mongodb/mongodb-agent@sha256:ghi789jkl012": ("mongodb-agent-ubi", "1.0.0"),
            "quay.io/mongodb/ops-manager@sha256:jkl012mno345": ("ops-manager", "1.0.0"),
        }

        for img_url, tag in sorted(digest_images.items()):
            print(f"    {img_url} -> {tag}")
            # Verify the backup tag is generated correctly
            repo_name, version = expected_tags[img_url]
            expected_backup = f"quay.io/mongodb/{repo_name}:{tag}_openshift_{version}"

            backup_tag = generate_backup_tag(img_url, tag, version)
            assert backup_tag == expected_backup, f"Expected {expected_backup}, got {backup_tag}"

        # All images in our test should be digest-pinned
        assert len(digest_images) == len(all_images)

    finally:
        # Clean up
        temp_path.unlink()


class TestBackupImageProcess(unittest.TestCase):
    @patch("subprocess.run")
    def test_backup_image_process_success(self, mock_run):
        # Mock subprocess.run to return a successful result
        mock_result = MagicMock()
        mock_result.returncode = 0
        mock_run.return_value = mock_result

        # Test with a digest-pinned image
        original_image = "quay.io/mongodb/mongodb-enterprise-server@sha256:abc123"
        backup_image = "quay.io/mongodb/mongodb-enterprise-server:4.4.15-ubi8_openshift_1.2.0"

        result = backup_image_process(original_image, backup_image, dry_run=False)

        # Verify the function returns True for success
        self.assertTrue(result)

        # Verify the correct commands were executed
        expected_calls = [
            (["docker", "manifest", "inspect", backup_image],),
            (["docker", "pull", original_image],),
            (["docker", "tag", original_image, backup_image],),
            (["docker", "push", backup_image],),
        ]

        for i, call_args in enumerate(mock_run.call_args_list):
            self.assertEqual(call_args[0][0], expected_calls[i][0])

    @patch("subprocess.run")
    def test_backup_image_process_dry_run(self, mock_run):
        # Test with dry run enabled
        original_image = "quay.io/mongodb/mongodb-enterprise-server@sha256:abc123"
        backup_image = "quay.io/mongodb/mongodb-enterprise-server:4.4.15-ubi8_openshift_1.2.0"

        result = backup_image_process(original_image, backup_image, dry_run=True)

        # Should still return True in dry run mode
        self.assertTrue(result)
        # No actual commands should be executed
        mock_run.assert_not_called()

    @patch("subprocess.run")
    def test_backup_image_process_no_digest(self, mock_run):
        # Test with an image that has no digest
        original_image = "quay.io/mongodb/mongodb-enterprise-server:4.4.15-ubi8"
        backup_image = "quay.io/mongodb/mongodb-enterprise-server:4.4.15-ubi8_openshift_1.2.0"

        result = backup_image_process(original_image, backup_image, dry_run=False)

        # Should return False and not execute any commands
        self.assertFalse(result)
        mock_run.assert_not_called()

    @patch("subprocess.run")
    def test_backup_image_process_failure(self, mock_run):
        # Test with a failing command
        mock_run.side_effect = subprocess.CalledProcessError(1, "docker pull")

        original_image = "quay.io/mongodb/mongodb-enterprise-server@sha256:abc123"
        backup_image = "quay.io/mongodb/mongodb-enterprise-server:4.4.15-ubi8_openshift_1.2.0"

        result = backup_image_process(original_image, backup_image, dry_run=False)

        # Should return False on failure
        self.assertFalse(result)


class TestRunCommand(unittest.TestCase):
    @patch("subprocess.run")
    def test_run_command_success(self, mock_run):
        # Mock a successful command execution
        mock_result = MagicMock()
        mock_result.returncode = 0
        mock_result.stdout = "test output"
        mock_run.return_value = mock_result

        result = run_command(["echo", "test"], dry_run=False)

        self.assertEqual(result, mock_result)
        mock_run.assert_called_once()

    @patch("subprocess.run")
    def test_run_command_dry_run(self, mock_run):
        # Test dry run mode
        result = run_command(["echo", "test"], dry_run=True)

        self.assertIsNone(result)
        mock_run.assert_not_called()

    @patch("subprocess.run")
    def test_run_command_failure(self, mock_run):
        # Test command failure with check=True (default)
        mock_run.side_effect = subprocess.CalledProcessError(1, "test command")

        with self.assertRaises(subprocess.CalledProcessError):
            run_command(["false"])

    @patch("subprocess.run")
    def test_run_command_no_check(self, mock_run):
        # Test command failure with check=False
        mock_result = MagicMock()
        mock_result.returncode = 1
        mock_result.stdout = ""
        mock_result.stderr = "Command failed"
        mock_run.return_value = mock_result

        result = run_command(["false"], check=False)
        self.assertIsNotNone(result)
        self.assertEqual(result.returncode, 1)


def main():
    """Run all tests."""
    print("Running CSV image backup script tests...\n")

    # Run unittests
    test_suite = unittest.TestLoader().loadTestsFromTestCase(TestBackupImageProcess)
    unittest.TextTestRunner(verbosity=2).run(test_suite)

    test_suite = unittest.TestLoader().loadTestsFromTestCase(TestRunCommand)
    unittest.TextTestRunner(verbosity=2).run(test_suite)

    # Run existing tests
    test_parse_image_url()
    test_backup_tag_generation()
    test_csv_parsing()

    print("\nAll tests passed! ✓")
    print("\nTo use the backup script:")
    print("  python backup_csv_images.py /path/to/clusterserviceversion.yaml")
    print("  python backup_csv_images.py --dry-run /path/to/clusterserviceversion.yaml")
    print("  python backup_csv_images.py --all-images /path/to/clusterserviceversion.yaml")


if __name__ == "__main__":
    main()
