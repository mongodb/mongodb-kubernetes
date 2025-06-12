import os
import shutil
import tempfile

from _pytest.fixtures import fixture
from git import Repo
from scripts.release.changelog import CHANGELOG_PATH


@fixture(scope="session")
def git_repo(change_log_path: str = CHANGELOG_PATH) -> Repo:
    """Create a temporary git repository for testing."""

    repo_dir = tempfile.mkdtemp()
    repo = Repo.init(repo_dir)
    changelog_path = os.path.join(repo_dir, change_log_path)
    os.mkdir(changelog_path)

    ## First commit and 1.0.0 tag
    repo.git.checkout("-b", "master")
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

    ## Static architecture release and 2.0.0 tag
    changelog_file = add_file(repo_dir, "changelog/20250612_breaking_static_as_default.md")
    repo.index.add(changelog_file)
    repo.index.commit("Static architecture as default")
    changelog_file = add_file(repo_dir, "changelog/20250616_feature_om_no_service_mesh.md")
    repo.index.add(changelog_file)
    repo.index.commit("Ops Manager no service mesh support")
    changelog_file_1 = add_file(repo_dir, "changelog/20250620_fix_static_container.md")
    changelog_file_2 = add_file(repo_dir, "changelog/20250622_fix_external_access.md")
    repo.index.add([changelog_file_1, changelog_file_2])
    fix_commit = repo.index.commit("Fixes for static architecture")
    changelog_file = add_file(repo_dir, "changelog/20250623_prelude_static.md")
    repo.index.add(changelog_file)
    repo.index.commit("Release notes prelude for static architecture")
    repo.create_tag("2.0.0", message="Static architecture release")

    ## Create release-1.x branch and backport fix
    repo.git.checkout("1.2.0")
    release_1_x_branch = repo.create_head("release-1.x").checkout()
    repo.git.cherry_pick(fix_commit.hexsha)
    repo.create_tag("1.2.1", message="Bug fix release")

    ## Bug fixes and 2.0.1 tag
    repo.git.checkout("master")
    file_name = create_new_file(repo_dir, "bugfix-placeholder.go", "Bugfix in go\n")
    changelog_file = add_file(repo_dir, "changelog/20250701_fix_placeholder.md")
    repo.index.add([file_name, changelog_file])
    fix_commit_1 = repo.index.commit("placeholder fix")
    changelog_file = add_file(repo_dir, "changelog/20250702_fix_clusterspeclist_validation.md")
    repo.index.add(changelog_file)
    fix_commit_2 = repo.index.commit("fix clusterspeclist validation")
    repo.create_tag("2.0.1", message="Bug fix release")

    ## Backport fixes to release-1.x branch
    repo.git.checkout(release_1_x_branch)
    repo.git.cherry_pick(fix_commit_1.hexsha)
    repo.git.cherry_pick(fix_commit_2.hexsha)
    repo.create_tag("1.2.2", message="Bug fix release")

    ## Bug fix and 2.0.2 tag
    repo.git.checkout("master")
    changelog_file = add_file(repo_dir, "changelog/20250707_fix_proxy_env_var.md")
    repo.index.add(changelog_file)
    fix_commit = repo.index.commit("fix proxy env var validation")
    repo.create_tag("2.0.2", message="Bug fix release")

    ## Backport fixes to release-1.x branch
    repo.git.checkout(release_1_x_branch)
    repo.git.cherry_pick(fix_commit)
    repo.create_tag("1.2.3", message="Bug fix release")

    ## Static architecture release and 3.0.0 tag
    repo.git.checkout("master")
    changelog_file_1 = add_file(repo_dir, "changelog/20250710_breaking_mongodbmulti_refactor.md")
    changelog_file_2 = add_file(repo_dir, "changelog/20250710_prelude_mongodbmulti_refactor.md")
    repo.index.add([changelog_file_1, changelog_file_2])
    repo.index.commit("Moved MongoDBMulti into single MongoDB resource")
    changelog_file = add_file(repo_dir, "changelog/20250711_feature_public_search.md")
    repo.index.add(changelog_file)
    repo.index.commit("Public search support")
    changelog_file = add_file(repo_dir, "changelog/20250712_fix_mongodbuser_phase.md")
    repo.index.add(changelog_file)
    fix_commit = repo.index.commit("MongoDBUser phase update fix")
    repo.create_tag("3.0.0", message="MongoDBMulti integration with MongoDB resource")

    ## Create release-2.x branch and backport fix
    repo.git.checkout("2.0.2")
    release_2_x_branch = repo.create_head("release-2.x").checkout()
    repo.git.cherry_pick(fix_commit.hexsha)
    repo.create_tag("2.0.3", message="Bug fix release")

    ## Backport fixes to release-1.x branch
    fix_commit = release_2_x_branch.commit
    repo.git.checkout(release_1_x_branch)
    repo.git.cherry_pick(fix_commit)
    repo.create_tag("1.2.4", message="Bug fix release")

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
