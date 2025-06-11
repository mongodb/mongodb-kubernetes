import os
import shutil
import tempfile

from _pytest.fixtures import fixture
from git import Repo
from scripts.release.changelog import CHANGELOG_PATH


@fixture(scope="module")
def git_repo(change_log_path: str = CHANGELOG_PATH) -> Repo:
    """Create a temporary git repository for testing."""

    repo_dir = tempfile.mkdtemp()
    repo = Repo.init(repo_dir)
    changelog_path = os.path.join(repo_dir, change_log_path)
    os.mkdir(changelog_path)

    ## First commit and 1.0.0 tag
    new_file = create_new_file(repo_dir, "new-file.txt", "Initial content\n")
    changelog_file = add_file(repo_dir, "changelog/20250506_prelude_mck.md")
    repo.index.add([new_file, changelog_file])
    repo.index.commit("initial commit")
    repo.create_tag("1.0.0", message="Initial release")

    ## Bug fixes and 1.0.1 tag
    file_name = create_new_file(repo_dir, "another-file.txt", "Added more content\n")
    changelog_file = add_file(repo_dir, "changelog/20250510_fix_olm_missing_images.md")
    repo.index.add([file_name, changelog_file])
    repo.index.commit("olm missing images fix")
    changelog_file = add_file(repo_dir, "changelog/20250510_fix_watched_list_in_helm.md")
    repo.index.add(changelog_file)
    repo.index.commit("fix watched list in helm")
    repo.create_tag("1.0.1", message="Bug fix release")

    ## Private search preview and 1.1.0 tag (with changelog fix)
    changelog_file = add_file(repo_dir, "changelog/20250523_feature_community_search_preview.md")
    repo.index.add(changelog_file)
    repo.index.commit("private search preview")
    changelog_file = add_file(
        repo_dir,
        "changelog/20250523_feature_community_search_preview_UPDATED.md",
        "changelog/20250523_feature_community_search_preview.md"
    )
    repo.index.add(changelog_file)
    repo.index.commit("add limitations in changelog for private search preview")
    repo.create_tag("1.1.0", message="Public search preview release")

    ## OIDC release and 1.2.0 tag
    changelog_file = add_file(repo_dir, "changelog/20250610_feature_oidc.md")
    repo.index.add(changelog_file)
    repo.index.commit("OIDC integration")
    repo.create_tag("1.2.0", message="OIDC integration release")

    return repo


def create_new_file(repo_path: str, file_path: str, file_content: str):
    """Create a new file in the repository."""

    file_name = os.path.join(repo_path, file_path)
    with open(file_name, "a") as f:
        f.write(file_content)

    return file_name


def add_file(repo_path: str, src_file_path: str, dst_file_path: str | None = None):
    """Adds a file in the repository path."""

    if not dst_file_path:
        dst_file_path = src_file_path

    dst_path = os.path.join(repo_path, dst_file_path)
    src_path = os.path.join('scripts/release/testdata', src_file_path)

    return shutil.copy(src_path, dst_path)
