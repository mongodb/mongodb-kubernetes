import os

import changelog
from git import Repo
import tempfile

from scripts.release.changelog import CHANGELOG_PATH


def create_git_repo():
    """Create a temporary git repository for testing."""

    repo_dir = tempfile.mkdtemp()
    repo = Repo.init(repo_dir)

    ## First commit
    file_name = create_new_file(repo_dir, "new-file.txt", "Initial content\n")
    repo.index.add([file_name])
    repo.index.commit("initial commit")
    repo.create_tag("1.0.0", message="Initial release")

    ## Second commit
    file_name = create_new_file(repo_dir, "another-file.txt", "Added more content\n")
    repo.index.add([file_name])
    repo.index.commit("additional changes")

    changelog_path = os.path.join(repo_dir, CHANGELOG_PATH)
    os.mkdir(changelog_path)
    file_name = create_new_file(repo_dir, "changelog/20250610_feature_oidc.md", """
    * **MongoDB**, **MongoDBMulti**: Added support for OpenID Connect (OIDC) user authentication.
      * OIDC authentication can be configured with `spec.security.authentication.modes=OIDC` and `spec.security.authentication.oidcProviderConfigs` settings.
      * Minimum MongoDB version requirements:
        * `7.0.11`, `8.0.0`
        * Only supported with MongoDB Enterprise Server
      * For more information please see:
        * [Secure Client Authentication with OIDC](https://www.mongodb.com/docs/kubernetes/upcoming/tutorial/secure-client-connections/)
        * [Manage Database Users using OIDC](https://www.mongodb.com/docs/kubernetes/upcoming/manage-users/)
        * [Authentication and Authorization with OIDC/OAuth 2.0](https://www.mongodb.com/docs/manual/core/oidc/security-oidc/)
    """)
    repo.index.add([file_name])

    return repo, repo_dir

def create_new_file(repo_path: str, file_path: str, file_content: str):
    """Create a new file in the repository."""
    file_name = os.path.join(repo_path, file_path)
    with open(file_name, "a") as f:
        f.write(file_content)

    return file_name

def test_get_changelog_entries():
    repo, repo_path = create_git_repo()
    entries = changelog.get_changelog_entries("1.0.0", repo_path)
