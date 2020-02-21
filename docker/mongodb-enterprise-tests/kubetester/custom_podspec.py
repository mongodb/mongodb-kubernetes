from typing import List, Optional


def assert_stateful_set_podspec(
    pod_template_spec,
    weight: int = 0,
    topology_key: str = "",
    grace_period_seconds: int = 0,
    containers_spec: Optional[List] = None,
) -> None:
    assert pod_template_spec.termination_grace_period_seconds == grace_period_seconds
    assert (
        pod_template_spec.affinity.pod_anti_affinity.preferred_during_scheduling_ignored_during_execution[
            0
        ].weight
        == weight
    )
    assert (
        pod_template_spec.affinity.pod_anti_affinity.preferred_during_scheduling_ignored_during_execution[
            0
        ].pod_affinity_term.topology_key
        == topology_key
    )
    if containers_spec is None:
        containers_spec = []
    for i, expected_spec in enumerate(containers_spec):
        spec = pod_template_spec.containers[i].to_dict()
        # compare only the expected keys
        for k in expected_spec:
            assert expected_spec[k] == spec[k]
