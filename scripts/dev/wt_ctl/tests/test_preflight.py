from wt_ctl.preflight import target_commit
from wt_ctl.runner import Runner


def test_the_target_commit_is_the_local_or_remote_branch_and_else_head(tmp_path):
    def git(*args):
        return Runner().run(["git", "-c", "user.name=t", "-c", "user.email=t@t", *args], cwd=tmp_path).stdout.strip()

    git("init", "-q")
    git("commit", "-q", "--allow-empty", "-m", "old")
    old = git("rev-parse", "HEAD")
    git("commit", "-q", "--allow-empty", "-m", "new")
    git("branch", "local-branch", old)
    git("update-ref", "refs/remotes/fork/remote-branch", old)

    targets = [target_commit(Runner(), tmp_path, b) for b in ("local-branch", "remote-branch", "new-branch")]
    assert targets == [old, old, "HEAD"]
    assert target_commit(Runner(), tmp_path / "missing", "new-branch") == "HEAD"
