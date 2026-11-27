from wt_ctl.domains.devcontainer import own_volumes


def test_own_volumes_keeps_the_worktree_volumes_and_leaves_the_shared_caches():
    labelled = [
        "a_devcontainer_venv",
        "a_devcontainer_dind-var-lib-docker-0abc",
        "mck-devcontainer-gomodcache",
    ]

    assert own_volumes("a_devcontainer", labelled) == ["a_devcontainer_venv", "a_devcontainer_dind-var-lib-docker-0abc"]
