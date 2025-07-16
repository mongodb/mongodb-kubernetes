from changelog import get_change_type, ChangeType


def test_get_change_type():

    # Test prelude
    assert get_change_type("20250502_prelude_release_notes.md") == ChangeType.PRELUDE

    # Test breaking changes
    assert get_change_type("20250101_breaking_change_api_update.md") == ChangeType.BREAKING
    assert get_change_type("20250508_breaking_remove_deprecated.md") == ChangeType.BREAKING
    assert get_change_type("20250509_major_schema_change.md") == ChangeType.BREAKING

    # Test features
    assert get_change_type("20250509_feature_new_dashboard.md") == ChangeType.FEATURE
    assert get_change_type("20250511_feat_add_metrics.md") == ChangeType.FEATURE

    # Test fixes
    assert get_change_type("20251210_fix_olm_missing_images.md") == ChangeType.FIX
    assert get_change_type("20251010_bugfix_memory_leak.md") == ChangeType.FIX
    assert get_change_type("20250302_hotfix_security_issue.md") == ChangeType.FIX
    assert get_change_type("20250301_patch_typo_correction.md") == ChangeType.FIX

    # Test other
    assert get_change_type("20250520_docs_update_readme.md") == ChangeType.OTHER
    assert get_change_type("20250610_refactor_codebase.md") == ChangeType.OTHER
