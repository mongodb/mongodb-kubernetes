import datetime
import os
import re
from enum import StrEnum

import frontmatter
from git import Commit, Repo

DEFAULT_CHANGELOG_PATH = "changelog/"
DEFAULT_INITIAL_GIT_TAG_VERSION = "1.0.0"
FILENAME_DATE_FORMAT = "%Y%m%d"
FRONTMATTER_DATE_FORMAT = "%Y-%m-%d"

PRELUDE_ENTRIES = ["prelude"]
BREAKING_CHANGE_ENTRIES = ["breaking", "major"]
FEATURE_ENTRIES = ["feat", "feature"]
BUGFIX_ENTRIES = ["fix", "bugfix", "hotfix", "patch"]


class ChangeKind(StrEnum):
    PRELUDE = "prelude"
    BREAKING = "breaking"
    FEATURE = "feature"
    FIX = "fix"
    OTHER = "other"


class ChangeMeta:
    def __init__(self, date: datetime, kind: ChangeKind, title: str):
        self.date = date
        self.kind = kind
        self.title = title


def get_changelog_entries(
    previous_version_commit: Commit,
    repo: Repo,
    changelog_sub_path: str,
) -> list[tuple[ChangeKind, str]]:
    changelog = []

    # Compare previous version commit with current working tree
    diff_index = previous_version_commit.diff(other=repo.head.commit, paths=changelog_sub_path)

    # No changes since the previous version
    if not diff_index:
        return changelog

    # Traverse added Diff objects only (change type 'A' for added files)
    for diff_item in diff_index.iter_change_type("A"):
        file_path = diff_item.b_path

        change_meta, contents = extract_changelog_data(repo.working_dir, file_path)

        changelog.append((str(change_meta.kind), contents))

    return changelog


def extract_changelog_data(working_dir: str, file_path: str) -> (ChangeMeta, str):
    file_name = os.path.basename(file_path)
    date, kind = extract_date_and_kind_from_file_name(file_name)

    abs_file_path = os.path.join(working_dir, file_path)
    with open(abs_file_path, "r") as file:
        file_content = file.read()

    change_meta, contents = strip_changelog_entry_frontmatter(file_content)

    if change_meta.date != date:
        raise Exception(
            f"{file_name} - date in front matter {change_meta.date} does not match date extracted from file name {date}"
        )

    if change_meta.kind != kind:
        raise Exception(
            f"{file_name} - kind in front matter {change_meta.kind} does not match kind extracted from file name {kind}"
        )

    return change_meta, contents


def extract_date_and_kind_from_file_name(file_name: str) -> (datetime, ChangeKind):
    match = re.match(r"(\d{8})_([a-zA-Z]+)_(.+)\.md", file_name)
    if not match:
        raise Exception(f"{file_name} - doesn't match expected pattern")

    date_str, kind_str, _ = match.groups()
    try:
        date = parse_change_date(date_str, FILENAME_DATE_FORMAT)
    except Exception as e:
        raise Exception(f"{file_name} - {e}")

    kind = get_change_kind(kind_str)

    return date, kind


def parse_change_date(date_str: str, date_format: str) -> datetime:
    try:
        date = datetime.datetime.strptime(date_str, date_format).date()
    except Exception:
        raise Exception(f"date {date_str} is not in the expected format {date_format}")

    return date


def get_change_kind(kind_str: str) -> ChangeKind:
    if kind_str.lower() in PRELUDE_ENTRIES:
        return ChangeKind.PRELUDE
    if kind_str.lower() in BREAKING_CHANGE_ENTRIES:
        return ChangeKind.BREAKING
    elif kind_str.lower() in FEATURE_ENTRIES:
        return ChangeKind.FEATURE
    elif kind_str.lower() in BUGFIX_ENTRIES:
        return ChangeKind.FIX
    else:
        return ChangeKind.OTHER


def strip_changelog_entry_frontmatter(file_contents: str) -> (ChangeMeta, str):
    """Strip the front matter from a changelog entry."""
    data = frontmatter.loads(file_contents)

    kind = get_change_kind(str(data["kind"]))
    date = parse_change_date(str(data["date"]), FRONTMATTER_DATE_FORMAT)
    meta = ChangeMeta(date=date, title=str(data["title"]), kind=kind)

    ## Add newline to contents so the Markdown file also contains a newline at the end
    contents = data.content + "\n"

    return meta, contents
