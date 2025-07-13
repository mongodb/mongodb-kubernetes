import datetime

import pytest
from changelog import (
    ChangeKind,
    extract_date_and_kind_from_file_name,
    strip_changelog_entry_frontmatter,
)


def test_extract_changelog_data_from_file_name():
    # Test prelude
    assert extract_date_and_kind_from_file_name("20250502_prelude_release_notes.md") == (
        datetime.date(2025, 5, 2),
        ChangeKind.PRELUDE,
    )

    # Test breaking changes
    assert extract_date_and_kind_from_file_name("20250101_breaking_api_update.md") == (
        datetime.date(2025, 1, 1),
        ChangeKind.BREAKING,
    )
    assert extract_date_and_kind_from_file_name("20250508_breaking_remove_deprecated.md") == (
        datetime.date(2025, 5, 8),
        ChangeKind.BREAKING,
    )
    assert extract_date_and_kind_from_file_name("20250509_major_schema_change.md") == (
        datetime.date(2025, 5, 9),
        ChangeKind.BREAKING,
    )

    # Test features
    assert extract_date_and_kind_from_file_name("20250509_feature_new_dashboard.md") == (
        datetime.date(2025, 5, 9),
        ChangeKind.FEATURE,
    )
    assert extract_date_and_kind_from_file_name("20250511_feat_add_metrics.md") == (
        datetime.date(2025, 5, 11),
        ChangeKind.FEATURE,
    )

    # Test fixes
    assert extract_date_and_kind_from_file_name("20251210_fix_olm_missing_images.md") == (
        datetime.date(2025, 12, 10),
        ChangeKind.FIX,
    )
    assert extract_date_and_kind_from_file_name("20251010_bugfix_memory_leak.md") == (
        datetime.date(2025, 10, 10),
        ChangeKind.FIX,
    )
    assert extract_date_and_kind_from_file_name("20250302_hotfix_security_issue.md") == (
        datetime.date(2025, 3, 2),
        ChangeKind.FIX,
    )
    assert extract_date_and_kind_from_file_name("20250301_patch_typo_correction.md") == (
        datetime.date(2025, 3, 1),
        ChangeKind.FIX,
    )

    # Test other
    assert extract_date_and_kind_from_file_name("20250520_docs_update_readme.md") == (
        datetime.date(2025, 5, 20),
        ChangeKind.OTHER,
    )
    assert extract_date_and_kind_from_file_name("20250610_refactor_codebase.md") == (
        datetime.date(2025, 6, 10),
        ChangeKind.OTHER,
    )

    # Invalid date part (day 40 does not exist)
    with pytest.raises(Exception) as e:
        extract_date_and_kind_from_file_name("20250640_refactor_codebase.md")
    assert str(e.value) == "20250640_refactor_codebase.md - date 20250640 is not in the expected format YYYYMMDD"

    # Wrong file name format (date part)
    with pytest.raises(Exception) as e:
        extract_date_and_kind_from_file_name("202yas_refactor_codebase.md")
    assert str(e.value) == "202yas_refactor_codebase.md - doesn't match expected pattern"

    # Wrong file name format (missing title part)
    with pytest.raises(Exception) as e:
        extract_date_and_kind_from_file_name("20250620_change.md")
    assert str(e.value) == "20250620_change.md - doesn't match expected pattern"


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
