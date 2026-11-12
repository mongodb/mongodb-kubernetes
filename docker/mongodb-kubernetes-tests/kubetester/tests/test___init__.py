import os
import unittest
from unittest.mock import MagicMock, patch

import kubernetes.client
from kubeobject import CustomObject
from kubetester.kubetester import (
    _static_container_skip_marker,
    assert_statefulset_architecture,
    get_migration_target_architecture,
)


class TestCreateOrUpdate(unittest.TestCase):
    def test_create_or_update_is_not_bound(self):
        api_client = MagicMock()
        custom_object = CustomObject(
            api_client=api_client,
            name="mock",
            namespace="mock",
            plural="mock",
            kind="mock",
            group="mock",
            version="v1",
        )
        custom_object.bound = False
        custom_object.create = MagicMock()

        custom_object.update()

        custom_object.create.assert_called_once()

    def test_create_or_update_is_not_bound_exists_update(self):
        api_client = MagicMock()
        custom_object = CustomObject(
            api_client=api_client,
            name="mock",
            namespace="mock",
            plural="mock",
            kind="mock",
            group="mock",
            version="v1",
        )
        custom_object.bound = False
        custom_object.create = MagicMock()
        custom_object.update = MagicMock()

        custom_object.create.side_effect = kubernetes.client.ApiException(status=409)
        custom_object.update()

        custom_object.update.assert_called_once()
        custom_object.create.assert_called_once()

    def test_create_or_update_is_bound_update(self):
        api_client = MagicMock()
        custom_object = CustomObject(
            api_client=api_client,
            name="mock",
            namespace="mock",
            plural="mock",
            kind="mock",
            group="mock",
            version="v1",
        )
        custom_object.bound = True
        custom_object.update = MagicMock()
        custom_object.load = MagicMock()

        custom_object.update()
        custom_object.update.assert_called_once()
        custom_object.load.assert_not_called()

    def test_create_or_update_is_bound_update_409_10_times(self):
        custom_object = CustomObject(
            api_client=MagicMock(),
            name="mock",
            namespace="mock",
            plural="mock",
            kind="mock",
            group="mock",
            version="v1",
        )
        loaded_object = CustomObject(
            api_client=MagicMock(),
            name="mock",
            namespace="mock",
            plural="mock",
            kind="mock",
            group="mock",
            version="v1",
        )
        loaded_object["spec"] = {"my": "test"}

        custom_object.bound = True
        custom_object.update = MagicMock()
        custom_object.api.get_namespaced_custom_object = MagicMock()
        custom_object.api.get_namespaced_custom_object.return_value = loaded_object

        exception_count = 0

        def raise_exception():
            nonlocal exception_count
            exception_count += 1
            raise kubernetes.client.ApiException(status=409)

        custom_object.update.side_effect = raise_exception

        with self.assertRaises(Exception) as context:
            custom_object.update()
        self.assertTrue("Tried client side merge" in str(context.exception))

        custom_object.update.assert_called()
        assert exception_count == 10

    def test_create_or_update_is_bound_update_409_few_times(self):
        api_client = MagicMock()
        object_to_api_server = CustomObject(
            api_client=api_client,
            name="mock",
            namespace="mock",
            plural="mock",
            kind="mock",
            group="mock",
            version="v1",
        )
        object_from_api_server = CustomObject(
            api_client=api_client,
            name="mock",
            namespace="mock",
            plural="mock",
            kind="mock",
            group="mock",
            version="v1",
        )

        object_to_api_server["spec"] = {"override": "test"}
        object_to_api_server["status"] = {"status": "pending"}

        object_from_api_server["spec"] = {"my": "test"}
        object_from_api_server["status"] = {"status": "running"}

        object_to_api_server.bound = True
        object_to_api_server.update = MagicMock()
        object_to_api_server.api.get_namespaced_custom_object = MagicMock()
        object_to_api_server.api.get_namespaced_custom_object.return_value = object_from_api_server

        exception_count = 0

        def raise_exception():
            nonlocal exception_count
            exception_count += 1
            if exception_count < 3:
                raise kubernetes.client.ApiException(status=409)

        object_to_api_server.update.side_effect = raise_exception

        object_to_api_server.update()
        object_to_api_server.update.assert_called()
        object_to_api_server.api.get_namespaced_custom_object.assert_called()

        # assert specs were taken from object_to_api_server
        assert object_to_api_server["spec"] == {"override": "test"}

        # assert status is taken from object_from_api_server
        assert object_to_api_server["status"] == {"status": "running"}


class TestStaticGAArchitecture(unittest.TestCase):
    def test_migration_target_and_skip_marker(self):
        for current, expected in (
            ("non-static", "static-ga"),
            ("static-ga", "non-static"),
        ):
            with self.subTest(current=current), patch.dict(os.environ, {"MDB_DEFAULT_ARCHITECTURE": current}):
                self.assertEqual(get_migration_target_architecture(), expected)
                self.assertFalse(_static_container_skip_marker().mark.args[0])

    def test_architecture_assertion_distinguishes_proxy_and_non_static(self):
        cases = (
            ("non-static", ["/opt/scripts/agent-launcher.sh"], None, None, False),
            ("static-ga", ["/mnt/proxy/proxy"], "proxy-image", None, False),
            (
                "static-ga",
                ["bash", "-c", "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"],
                None,
                True,
                True,
            ),
        )
        for architecture, command, proxy_volume, share_process_namespace, expect_error in cases:
            with self.subTest(architecture=architecture, command=command):
                containers = [kubernetes.client.V1Container(name="mongodb-enterprise-database", command=command)]
                init_containers = []
                volumes = []
                if architecture != "non-static":
                    agent_env = [
                        kubernetes.client.V1EnvVar(
                            name="MDB_STATIC_CONTAINERS_ARCHITECTURE",
                            value="true" if expect_error else "static-ga",
                        )
                    ]
                    if not expect_error:
                        agent_env.append(
                            kubernetes.client.V1EnvVar(
                                name="MDB_MONGODB_PROXY_ADDRESS",
                                value="127.0.0.1:8099",
                            )
                        )
                    containers.append(
                        kubernetes.client.V1Container(
                            name="mongodb-agent",
                            command=["/opt/scripts/agent-launcher.sh"],
                            env=agent_env,
                        )
                    )
                    init_containers.append(kubernetes.client.V1Container(name="mongodb-kubernetes-init-database"))
                if proxy_volume is not None:
                    volumes.append(
                        kubernetes.client.V1Volume(
                            name="mongodb-proxy",
                            image=kubernetes.client.V1ImageVolumeSource(reference=proxy_volume),
                        )
                    )

                statefulset = kubernetes.client.V1StatefulSet(
                    spec=kubernetes.client.V1StatefulSetSpec(
                        selector=kubernetes.client.V1LabelSelector(match_labels={"app": "test"}),
                        service_name="test",
                        template=kubernetes.client.V1PodTemplateSpec(
                            spec=kubernetes.client.V1PodSpec(
                                containers=containers,
                                init_containers=init_containers,
                                volumes=volumes,
                                share_process_namespace=share_process_namespace,
                            )
                        ),
                    )
                )
                if expect_error:
                    with self.assertRaises(AssertionError):
                        assert_statefulset_architecture(statefulset, architecture)
                else:
                    assert_statefulset_architecture(statefulset, architecture)

    def test_architecture_assertion_accepts_legacy_static(self):
        containers = [
            kubernetes.client.V1Container(
                name="mongodb-enterprise-database",
                command=["bash", "-c", "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"],
            ),
            kubernetes.client.V1Container(
                name="mongodb-agent",
                command=["/usr/local/bin/agent-launcher-shim.sh"],
                env=[kubernetes.client.V1EnvVar(name="MDB_STATIC_CONTAINERS_ARCHITECTURE", value="true")],
            ),
        ]

        def statefulset(share_process_namespace: bool) -> kubernetes.client.V1StatefulSet:
            return kubernetes.client.V1StatefulSet(
                spec=kubernetes.client.V1StatefulSetSpec(
                    selector=kubernetes.client.V1LabelSelector(match_labels={"app": "test"}),
                    service_name="test",
                    template=kubernetes.client.V1PodTemplateSpec(
                        spec=kubernetes.client.V1PodSpec(
                            containers=containers,
                            share_process_namespace=share_process_namespace,
                        )
                    ),
                )
            )

        assert_statefulset_architecture(statefulset(share_process_namespace=True), "static")
        with self.assertRaises(AssertionError):
            assert_statefulset_architecture(statefulset(share_process_namespace=False), "static")
