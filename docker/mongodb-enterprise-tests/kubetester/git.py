from git import Repo


def clone_and_checkout(url: str, fs_path: str, branch_name: str):
    repo = Repo.clone_from(url, fs_path)
    branch = repo.create_head(branch_name)
    branch.checkout()
