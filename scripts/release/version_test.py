import unittest
from scripts.release.version import calculate_next_release_version
from scripts.release.changelog import ChangeType


class TestCalculateNextReleaseVersion(unittest.TestCase):

    def test_bump_major_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.BREAKING]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_bump_minor_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.FEATURE]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_bump_patch_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.FIX]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_bump_patch_version_other_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.OTHER]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_bump_patch_version_no_changes(self):
        previous_version = "1.2.3"
        changelog = []
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_feature_takes_precedence(self):
        # Test that FEATURE has precedence over FIX
        previous_version = "1.2.3"
        changelog = [ChangeType.FEATURE, ChangeType.FIX]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_breaking_takes_precedence(self):
        # Test that BREAKING has precedence over FEATURE and FIX
        previous_version = "1.2.3"
        changelog = [ChangeType.FEATURE, ChangeType.BREAKING, ChangeType.FIX, ChangeType.OTHER]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_multiple_breaking_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.BREAKING, ChangeType.BREAKING, ChangeType.FEATURE, ChangeType.FIX, ChangeType.OTHER]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_multiple_feature_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.FEATURE, ChangeType.FEATURE, ChangeType.FIX, ChangeType.OTHER]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_multiple_fix_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.FIX, ChangeType.FIX, ChangeType.OTHER]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_multiple_other_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeType.OTHER, ChangeType.OTHER]
        next_version = calculate_next_release_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")
