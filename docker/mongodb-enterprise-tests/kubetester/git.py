from git import Repo


def clone_and_checkout(url: str, fs_path: str, branch_name: str):
    repo = Repo.clone_from(url, fs_path)
    repo.git.checkout(branch_name)
