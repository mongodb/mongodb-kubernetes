from kubernetes import client


def assert_pod_security_context(pod: client.V1Pod, managed: bool):
    sc = pod.spec.security_context

    if managed:
        assert sc.fs_group != 2000

        # se_linux_options is set on Openshift
        assert sc.se_linux_options.level is not None
        assert sc.se_linux_options.role is None
        assert sc.se_linux_options.type is None
        assert sc.se_linux_options.user is None
    else:
        assert sc.fs_group == 2000

        # In Kops and Kind se_linux_options is set to None
        assert sc.se_linux_options is None

    assert sc.run_as_group is None
    assert sc.run_as_non_root is None
    assert sc.run_as_user is None


def assert_pod_container_security_context(container: client.V1Container, managed: bool):
    sc = container.security_context

    if managed:
        assert sc.run_as_user != 2000
        assert sc.run_as_non_root is None
        assert sc.capabilities is not None
    else:
        # Only run_as_user and as_non_root are defined in
        # unmanaged_security_contexts
        assert sc.run_as_user == 2000
        assert sc.run_as_non_root
        assert sc.capabilities is None

    assert sc.allow_privilege_escalation is None
    assert sc.privileged is None
    assert sc.proc_mount is None
    assert sc.read_only_root_filesystem is None
    assert sc.se_linux_options is None
    assert sc.run_as_group is None
    assert sc.windows_options is None
