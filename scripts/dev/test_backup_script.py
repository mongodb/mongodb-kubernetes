#!/usr/bin/env python3

import tempfile
import yaml
from pathlib import Path
from backup_csv_images import (
    parse_csv_file,
    extract_images_from_csv,
    filter_digest_pinned_images,
    generate_backup_tag,
    parse_image_url
)

def get_test_csv():
    """Create a test ClusterServiceVersion with digest-pinned images."""
    test_csv = {
        "apiVersion": "operators.coreos.com/v1alpha1",
        "kind": "ClusterServiceVersion",
        "metadata": {
            "name": "test-operator.v1.0.0",
            "annotations": {
                "containerImage": "quay.io/test/operator@sha256:abc123def456"
            }
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
                                                        "value": "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789"
                                                    },
                                                    {
                                                        "name": "RELATED_IMAGE_AGENT",
                                                        "value": "quay.io/mongodb/mongodb-agent-ubi@sha256:ghi789jkl012"
                                                    },
                                                    {
                                                        "name": "REGULAR_ENV_VAR",
                                                        "value": "not-an-image"
                                                    }
                                                ]
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
                {
                    "name": "database-image",
                    "image": "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789"
                },
                {
                    "name": "agent-image",
                    "image": "quay.io/mongodb/mongodb-agent-ubi@sha256:ghi789jkl012"
                },
                {
                    "name": "ops-manager-image",
                    "image": "quay.io/mongodb/ops-manager@sha256:jkl012mno345"
                }
            ]
        }
    }
    return test_csv

def test_parse_image_url():
    test_cases = [
        ("quay.io/mongodb/operator@sha256:abc123", ("quay.io", "mongodb/operator", "sha256:abc123")),
        ("registry.io/repo/image:v1.0.0", ("registry.io", "repo/image", "v1.0.0")),
        ("simple-image:latest", ("", "simple-image", "latest")),
        ("simple-image", ("", "simple-image", "latest")),
        ("quay.io/mongodb/mongodb-agent-ubi@sha256:def456",
         ("quay.io", "mongodb/mongodb-agent-ubi", "sha256:def456"))
    ]

    for image_url, expected in test_cases:
        result = parse_image_url(image_url)
        print(f"  {image_url} -> {result}")
        assert result == expected, f"Expected {expected}, got {result}"

def test_backup_tag_generation():
    """Test backup tag generation."""
    test_cases = [
        ("quay.io/mongodb/operator@sha256:abc123", "1.31.0", "1.0.0"),
        ("quay.io/mongodb/mongodb-agent-ubi@sha256:def456", "107.0.12.8669-1", "1.2.0"),
        ("quay.io/mongodb/mongodb-agent-ubi@sha256:ghi789", "12.0.33.7866_1.1.1.0", "1.2.0"),
        ("quay.io/mongodb/mongodb-enterprise-server@sha256:jkl012", "4.4.15-ubi8", "1.0.0")
    ]

    for image, original_tag, mck_version in test_cases:
        backup_tag = generate_backup_tag(image, original_tag, mck_version)
        print(f"  {image} (tag: {original_tag}, version: {mck_version}")
        print(f"    -> {backup_tag}")

        # Extract repo name from original image
        repo_name = image.split('@')[0].split('/')[-1]


        expected = f"quay.io/mongodb/{repo_name}:{original_tag}_openshift_{mck_version}"

        assert backup_tag == expected, f"Expected {expected}, got {backup_tag}"

def test_csv_parsing():
    """Test CSV parsing and image extraction."""

    # Create test CSV
    test_csv = get_test_csv()

    # Write to temporary file
    with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
        yaml.dump(test_csv, f)
        temp_path = Path(f.name)

    try:
        # Parse the CSV
        csv_data = parse_csv_file(temp_path)
        assert csv_data is not None

        # Extract images
        all_images = extract_images_from_csv(csv_data)

        # Should find 4 unique images (operator appears in both deployment and annotation)
        expected_image_urls = {
            "quay.io/test/operator@sha256:abc123def456",
            "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789",
            "quay.io/mongodb/mongodb-agent-ubi@sha256:ghi789jkl012",
            "quay.io/mongodb/ops-manager@sha256:jkl012mno345"
        }
        assert set(all_images.keys()) == expected_image_urls, f"Expected {expected_image_urls}, got {set(all_images.keys())}"

        # Filter digest-pinned images
        digest_images = filter_digest_pinned_images(all_images)
        print(f"  Found {len(digest_images)} digest-pinned images:")

        # Expected backup tags based on the new format
        expected_tags = {
            "quay.io/test/operator@sha256:abc123def456": "1.0.0",  # From metadata.name
            "quay.io/mongodb/mongodb-enterprise-server@sha256:def456ghi789": "1.0.0",
            "quay.io/mongodb/mongodb-agent-ubi@sha256:ghi789jkl012": "1.0.0",
            "quay.io/mongodb/ops-manager@sha256:jkl012mno345": "1.0.0"
        }

        for img_url, tag in sorted(digest_images.items()):
            print(f"    {img_url} -> {tag}")
            # Verify the backup tag is generated correctly
            repo_name = img_url.split('@')[0].split('/')[-1]
            expected_version = expected_tags[img_url]
            expected_backup = f"quay.io/mongodb/{repo_name}:{expected_version}_openshift"

            backup_tag = generate_backup_tag(img_url, tag, expected_version)
            assert backup_tag == expected_backup, f"Expected {expected_backup}, got {backup_tag}"

        # All images in our test should be digest-pinned
        assert len(digest_images) == len(all_images)

    finally:
        # Clean up
        temp_path.unlink()

def main():
    """Run all tests."""
    print("Running CSV image backup script tests...\n")

    test_parse_image_url()
    print()

    test_backup_tag_generation()
    print()

    test_csv_parsing()
    print()

    print("All tests passed! ✓")
    print("\nTo use the backup script:")
    print("  python backup_csv_images.py /path/to/clusterserviceversion.yaml")
    print("  python backup_csv_images.py --dry-run /path/to/clusterserviceversion.yaml")
    print("  python backup_csv_images.py --all-images /path/to/clusterserviceversion.yaml")

if __name__ == "__main__":
    main()
