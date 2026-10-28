import pytest
from wt_ctl.create import DEFAULT_MULTI_CONTEXT, DEFAULT_SINGLE_CONTEXT, resolve_topology
from wt_ctl.errors import WtCtlError


def test_single_cluster_flag_with_a_multi_cluster_context_switches_to_the_default_single_context():
    assert resolve_topology(explicit=False, context=None, context_is_multi=True, recorded=None) == (
        False,
        DEFAULT_SINGLE_CONTEXT,
    )


def test_recorded_multi_cluster_topology_is_kept_without_a_flag():
    assert resolve_topology(explicit=None, context=None, context_is_multi=True, recorded=True) == (True, None)


def test_a_worktree_without_any_choice_is_multi_cluster():
    assert resolve_topology(explicit=None, context=None, context_is_multi=False, recorded=None) == (
        True,
        DEFAULT_MULTI_CONTEXT,
    )


def test_flag_and_context_of_the_other_topology_are_refused():
    with pytest.raises(WtCtlError, match="--single-cluster"):
        resolve_topology(explicit=False, context="e2e_multi_cluster_kind", context_is_multi=True, recorded=None)
