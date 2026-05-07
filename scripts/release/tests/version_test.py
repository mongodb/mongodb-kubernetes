import unittest

from scripts.release.changelog import ChangeKind
from scripts.release.version import increment_previous_version


class TestCalculateNextReleaseVersion(unittest.TestCase):

    def test_bump_major_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.BREAKING]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_bump_minor_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_bump_patch_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FIX]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_bump_patch_version_other_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_bump_patch_version_no_changes(self):
        previous_version = "1.2.3"
        changelog = []
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_feature_takes_precedence(self):
        # Test that FEATURE has precedence over FIX
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE, ChangeKind.FIX]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_breaking_takes_precedence(self):
        # Test that BREAKING has precedence over FEATURE and FIX
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE, ChangeKind.BREAKING, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_multiple_breaking_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.BREAKING, ChangeKind.BREAKING, ChangeKind.FEATURE, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_multiple_feature_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE, ChangeKind.FEATURE, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_multiple_fix_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FIX, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_multiple_other_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.OTHER, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")
