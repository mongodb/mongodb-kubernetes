import os
import unittest
from unittest.mock import MagicMock, patch

import kubernetes.client
from kubeobject import CustomObject
from kubetester.kubetester import (
    _static_container_skip_marker,
    assert_statefulset_architecture,
    get_default_architecture,
    get_migration_target_architecture,
    is_default_architecture_static,
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
    def test_default_architecture_preserves_static_ga(self):
        with patch.dict(os.environ, {"MDB_DEFAULT_ARCHITECTURE": "static-ga"}):
            self.assertEqual(get_default_architecture(), "static-ga")
            self.assertTrue(is_default_architecture_static())

    def test_migration_target_alternates_static_ga_and_non_static(self):
        for current, expected in (
            ("non-static", "static-ga"),
            ("static-ga", "non-static"),
            ("static", "non-static"),
        ):
            with self.subTest(current=current), patch.dict(os.environ, {"MDB_DEFAULT_ARCHITECTURE": current}):
                self.assertEqual(get_migration_target_architecture(), expected)

    def test_static_container_skip_only_applies_to_legacy_static(self):
        for architecture, expected_skip in (
            ("static", True),
            ("static-ga", False),
            ("non-static", False),
        ):
            with (
                self.subTest(architecture=architecture),
                patch.dict(os.environ, {"MDB_DEFAULT_ARCHITECTURE": architecture}),
            ):
                marker = _static_container_skip_marker()
                self.assertEqual(marker.mark.args[0], expected_skip)

    def test_architecture_assertion_distinguishes_proxy_and_legacy_modes(self):
        for architecture, command, proxy_volume, share_process_namespace in (
            ("non-static", ["/opt/scripts/agent-launcher.sh"], None, None),
            ("static", ["bash", "-c", "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"], None, True),
            ("static-ga", ["/mnt/proxy/proxy"], "proxy-image", None),
        ):
            with self.subTest(architecture=architecture):
                containers = [kubernetes.client.V1Container(name="mongodb-enterprise-database", command=command)]
                volumes = []
                if architecture != "non-static":
                    agent_env = [
                        kubernetes.client.V1EnvVar(
                            name="MDB_STATIC_CONTAINERS_ARCHITECTURE",
                            value="true",
                        )
                    ]
                    if architecture == "static-ga":
                        agent_env.append(
                            kubernetes.client.V1EnvVar(
                                name="MDB_MONGODB_PROXY_ADDRESS",
                                value="127.0.0.1:8099",
                            )
                        )
                    containers.append(kubernetes.client.V1Container(name="mongodb-agent", env=agent_env))
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
                                volumes=volumes,
                                share_process_namespace=share_process_namespace,
                            )
                        ),
                    )
                )
                assert_statefulset_architecture(statefulset, architecture)

    def test_architecture_assertion_rejects_legacy_static_in_static_ga_phase(self):
        statefulset = kubernetes.client.V1StatefulSet(
            spec=kubernetes.client.V1StatefulSetSpec(
                selector=kubernetes.client.V1LabelSelector(match_labels={"app": "test"}),
                service_name="test",
                template=kubernetes.client.V1PodTemplateSpec(
                    spec=kubernetes.client.V1PodSpec(
                        containers=[
                            kubernetes.client.V1Container(
                                name="mongodb",
                                command=[
                                    "bash",
                                    "-c",
                                    "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker",
                                ],
                            ),
                            kubernetes.client.V1Container(
                                name="mongodb-agent",
                                env=[
                                    kubernetes.client.V1EnvVar(
                                        name="MDB_STATIC_CONTAINERS_ARCHITECTURE",
                                        value="true",
                                    )
                                ],
                            ),
                        ],
                        share_process_namespace=True,
                    )
                ),
            )
        )
        with self.assertRaises(AssertionError):
            assert_statefulset_architecture(statefulset, "static-ga")
