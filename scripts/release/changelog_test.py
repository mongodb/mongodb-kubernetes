import datetime
import unittest

from changelog import (
    MAX_TITLE_LENGTH,
    ChangeKind,
    extract_date_and_kind_from_file_name,
    sanitize_title,
    strip_changelog_entry_frontmatter,
)


class TestExtractChangelogDataFromFileName(unittest.TestCase):
    def test_prelude(self):
        date, kind = extract_date_and_kind_from_file_name("20250502_prelude_release_notes.md")
        self.assertEqual(date, datetime.date(2025, 5, 2))
        self.assertEqual(kind, ChangeKind.PRELUDE)

    def test_breaking_changes(self):
        date, kind = extract_date_and_kind_from_file_name("20250101_breaking_api_update.md")
        self.assertEqual(date, datetime.date(2025, 1, 1))
        self.assertEqual(kind, ChangeKind.BREAKING)

        date, kind = extract_date_and_kind_from_file_name("20250508_breaking_remove_deprecated.md")
        self.assertEqual(date, datetime.date(2025, 5, 8))
        self.assertEqual(kind, ChangeKind.BREAKING)

        date, kind = extract_date_and_kind_from_file_name("20250509_major_schema_change.md")
        self.assertEqual(date, datetime.date(2025, 5, 9))
        self.assertEqual(kind, ChangeKind.BREAKING)

    def test_features(self):
        date, kind = extract_date_and_kind_from_file_name("20250509_feature_new_dashboard.md")
        self.assertEqual(date, datetime.date(2025, 5, 9))
        self.assertEqual(kind, ChangeKind.FEATURE)

        date, kind = extract_date_and_kind_from_file_name("20250511_feat_add_metrics.md")
        self.assertEqual(date, datetime.date(2025, 5, 11))
        self.assertEqual(kind, ChangeKind.FEATURE)

    def test_fixes(self):
        date, kind = extract_date_and_kind_from_file_name("20251210_fix_olm_missing_images.md")
        self.assertEqual(date, datetime.date(2025, 12, 10))
        self.assertEqual(kind, ChangeKind.FIX)

        date, kind = extract_date_and_kind_from_file_name("20251010_bugfix_memory_leak.md")
        self.assertEqual(date, datetime.date(2025, 10, 10))
        self.assertEqual(kind, ChangeKind.FIX)

        date, kind = extract_date_and_kind_from_file_name("20250302_hotfix_security_issue.md")
        self.assertEqual(date, datetime.date(2025, 3, 2))
        self.assertEqual(kind, ChangeKind.FIX)

        date, kind = extract_date_and_kind_from_file_name("20250301_patch_typo_correction.md")
        self.assertEqual(date, datetime.date(2025, 3, 1))
        self.assertEqual(kind, ChangeKind.FIX)

    def test_other(self):
        date, kind = extract_date_and_kind_from_file_name("20250520_docs_update_readme.md")
        self.assertEqual(date, datetime.date(2025, 5, 20))
        self.assertEqual(kind, ChangeKind.OTHER)

        date, kind = extract_date_and_kind_from_file_name("20250610_refactor_codebase.md")
        self.assertEqual(date, datetime.date(2025, 6, 10))
        self.assertEqual(kind, ChangeKind.OTHER)

    def test_invalid_date(self):
        with self.assertRaises(Exception) as context:
            extract_date_and_kind_from_file_name("20250640_refactor_codebase.md")
        self.assertEqual(
            str(context.exception), "20250640_refactor_codebase.md - date 20250640 is not in the expected format %Y%m%d"
        )

    def test_wrong_file_name_format_date(self):
        with self.assertRaises(Exception) as context:
            extract_date_and_kind_from_file_name("202yas_refactor_codebase.md")
        self.assertEqual(str(context.exception), "202yas_refactor_codebase.md - doesn't match expected pattern")

    def test_wrong_file_name_format_missing_title(self):
        with self.assertRaises(Exception) as context:
            extract_date_and_kind_from_file_name("20250620_change.md")
        self.assertEqual(str(context.exception), "20250620_change.md - doesn't match expected pattern")


def test_strip_changelog_entry_frontmatter():
    file_contents = """
---
title: This is my change
kind: feature
date: 2025-07-10
---

* **MongoDB**: public search preview release of MongoDB Search (Community Edition) is now available.
  * Added new property [spec.search](https://www.mongodb.com/docs/kubernetes/current/mongodb/specification/#spec-search) to enable MongoDB Search.
"""

    change_meta, contents = strip_changelog_entry_frontmatter(file_contents)

    assert change_meta.title == "This is my change"
    assert change_meta.kind == ChangeKind.FEATURE
    assert change_meta.date == datetime.date(2025, 7, 10)

    assert (
        contents
        == """* **MongoDB**: public search preview release of MongoDB Search (Community Edition) is now available.
  * Added new property [spec.search](https://www.mongodb.com/docs/kubernetes/current/mongodb/specification/#spec-search) to enable MongoDB Search.
"""
    )


class TestSanitizeTitle(unittest.TestCase):
    def test_basic_case(self):
        self.assertEqual(sanitize_title("Simple Title"), "simple_title")

    def test_non_alphabetic_chars(self):
        self.assertEqual(sanitize_title("Title tha@t-_ contain's strange char&s!"), "title_that_contains_strange_chars")

    def test_with_numbers_and_dashes(self):
        self.assertEqual(sanitize_title("Title with 123 numbers to-go!"), "title_with_123_numbers_to_go")

    def test_mixed_case(self):
        self.assertEqual(sanitize_title("MiXeD CaSe TiTlE"), "mixed_case_title")

    def test_length_limit(self):
        long_title = "This is a very long title that should be truncated because it exceeds the maximum length"
        sanitized_title = sanitize_title(long_title)
        self.assertTrue(len(sanitized_title) <= MAX_TITLE_LENGTH)
        self.assertEqual(sanitized_title, "this_is_a_very_long_title_that_should_be_truncated")

    def test_leading_trailing_spaces(self):
        sanitized_title = sanitize_title("  Title with spaces  ")
        self.assertEqual(sanitized_title, "title_with_spaces")

    def test_empty_title(self):
        self.assertEqual(sanitize_title(""), "")

    def test_only_non_alphabetic(self):
        self.assertEqual(sanitize_title("!@#"), "")
