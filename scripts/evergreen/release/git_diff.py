import sys

from git import Repo


def path_has_changes(path: str, tag_name: str) -> bool:
    """ Returns True if the current branch has differences for the 'path' comparing with
    the tag with name 'tag_name'
    """
    repo = Repo()
    commit_release = repo.commit(tag_name)
    commit_master = repo.commit()
    diff_index = commit_release.diff(commit_master, paths=path)

    return len(diff_index) > 0


if __name__ == "__main__":
    # execute only if run as a script
    print(sys.argv[1])
    print(sys.argv[2])
    print(path_has_changes(sys.argv[1], sys.argv[2]))
