from kubernetes import client


def assert_pod_security_context(pod: client.V1Pod, managed: bool):
    sc = pod.spec.security_context

    if managed:
        # Note, that this code is a bit fragile as may depend on the version of Openshift.
        assert sc.fs_group != 2000
        # se_linux_options is set on Openshift
        assert sc.se_linux_options.level is not None
        assert sc.se_linux_options.role is None
        assert sc.se_linux_options.type is None
        assert sc.run_as_non_root
        assert sc.run_as_user == 2000
    else:
        assert sc.fs_group == 2000
        # In Kops and Kind se_linux_options is set to None
        assert sc.se_linux_options is None

    assert sc.run_as_group is None


def assert_pod_container_security_context(container: client.V1Container, managed: bool):
    sc = container.security_context

    if managed:
        assert sc is None
    else:
        assert sc.read_only_root_filesystem
        assert sc.allow_privilege_escalation is False
