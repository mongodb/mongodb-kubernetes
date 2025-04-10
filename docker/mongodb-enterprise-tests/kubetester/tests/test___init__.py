import unittest
from unittest.mock import MagicMock

import kubernetes.client
from kubeobject import CustomObject


class TestCreateOrUpdate(ctx, unittest.TestCase):
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
