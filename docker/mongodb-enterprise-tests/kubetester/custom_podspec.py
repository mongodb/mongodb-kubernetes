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
        pod_template_spec.affinity.pod_anti_affinity.preferred_during_scheduling_ignored_during_execution[0].weight
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
            if k == "volume_mounts":
                assert_volume_mounts_are_equal(expected_spec[k], spec[k])
            else:
                assert expected_spec[k] == spec[k]


def assert_volume_mounts_are_equal(volume_mounts_1, volume_mounts_2):

    sorted_vols_1 = sorted(volume_mounts_1, key=lambda m: (m["name"], m["mount_path"]))
    sorted_vols_2 = sorted(volume_mounts_2, key=lambda m: (m["name"], m["mount_path"]))

    for vol1, vol2 in zip(sorted_vols_1, sorted_vols_2):
        assert vol1 == vol2
