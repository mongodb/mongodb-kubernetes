import copy
import io
from unittest.mock import Mock, call, patch

import pytest
import yaml
from box import Box
from kubeobject import KubeObject, create_custom_object
from kubernetes import client, config

# config.load_kube_config()


def test_box0():
    b = Box({"a": "first", "b": "second"})

    assert b.a == "first"
    assert b.b == "second"


def test_box1():
    """Adds a dict to a box"""

    b = Box({"a": {}})
    b.a = {"some": "data"}

    assert b.a.some == "data"


def test_box2():
    """Tries to use KubeObject as a proxy to a box"""
    k = KubeObject("group", "version", "plural")
    k.a = "attribute a"

    assert k.a == "attribute a"
    assert k.b.to_dict() == {}

    k.b = "now this works"
    assert k.b == "now this works"

    k.c = {"name": "some object"}
    assert k.c.name == "some object"

    # nested not yet defined attributes default to Box
    assert k.d.n.m.k == Box()

    k.d = {"n": {"m": {"k": "deep value"}}}

    # Now we can fetch the deep object using dot notation
    assert k.d.n.m.k == "deep value"

    # Also the previous to last key should be a box!
    assert isinstance(k.d.n.m, Box)

    # This will get a mutable reference to m
    borrow_box = k.d.n.m
    borrow_box.x = "and a final attribute"

    assert k.d.n.m.x == "and a final attribute"


def test_box3():
    """Tests that if an attribute exists, then calling it directly will not
    proxy the call through the getter."""
    k = KubeObject("group", "version", "plural")

    with pytest.raises(TypeError):
        # Raises because the default return for empty Box is not a callable
        k.whoami()

    whoami = Mock(return_value="I'm you")

    # but if we add it, then we get what we want
    k.whoami = whoami
    assert k.whoami() == "I'm you"

    whoami.assert_called_once()

    # TODO: Some magic methods are not easy to mock, see:
    #
    # https://docs.python.org/3/library/unittest.mock.html#mocking-magic-methods
    #
    # I would like to be able to test that __getattr__ was *not called*, but not
    # really sure how to do it. Not that you could change KubeObject's __getattr__ as
    #
    # KubeObject.__getattr__ = SOMETHING
    #
    # but we'll break the rest of the tests? better not to contaminate the tests
    # for now.


def test_box4():
    k = KubeObject("group", "version", "plural")

    k.a = {"hello": "hola"}

    assert k.a.hello == "hola"

    assert isinstance(k.a, Box)

    assert k.a["hello"] == "hola"

    assert isinstance(k.a, Box)
    assert isinstance(k["a"], dict)


def test_kubeobject0():
    k = KubeObject("group0", "version0", "plural0")
    assert k.crd == {"group": "group0", "version": "version0", "plural": "plural0"}


class CustomResource:
    def __init__(self, name):
        self.name = name


class MockedCustomObjectsApi:
    store = {
        "metadata": {"name": "my-dummy-object", "namespace": "default"},
        "spec": {"thisAttribute": "fourty two"},
    }

    @staticmethod
    def get_namespaced_custom_object(name, namespace, **kwargs):
        return MockedCustomObjectsApi.store

    @staticmethod
    def patch_namespaced_custom_object(name, namespace=None, group=None, version=None, plural=None, body=None):
        # body needs to be passed always
        assert body is not None
        MockedCustomObjectsApi.store = body

        return body

    @staticmethod
    def delete_namespaced_custom_object(group, version, namespace, plural, name, body):
        MockedCustomObjectsApi.store = {}


@patch("kubeobject.kubeobject.CustomObjectsApi")
def test_can_read_and_update(patched_custom_objects_api: Mock):
    api = Mock(wraps=MockedCustomObjectsApi)
    patched_custom_objects_api.return_value = api

    C = KubeObject("example.com", "v1", "dummies")
    c = C.read("my-dummy-object", "default")

    # Read API was called once
    api.get_namespaced_custom_object.assert_called_once()

    assert c.spec.thisAttribute == "fourty two"

    c.spec.thisAttribute = "fourty three"
    c.update()

    # We expect this body to be one sent to the API.
    updated_body = copy.deepcopy(MockedCustomObjectsApi.store)
    updated_body["spec"]["thisAttribute"] = "fourty three"
    api.patch_namespaced_custom_object.assert_called_once_with(
        **dict(
            name="my-dummy-object",
            namespace="default",
            plural="dummies",
            group="example.com",
            version="v1",
            body=updated_body,
        )
    )

    read_calls = [
        call(
            **dict(
                name="my-dummy-object",
                namespace="default",
                plural="dummies",
                group="example.com",
                version="v1",
            )
        ),
        call(
            **dict(
                name="my-dummy-object",
                namespace="default",
                plural="dummies",
                group="example.com",
                version="v1",
            )
        ),
    ]
    c1 = C.read("my-dummy-object", "default")
    # We expect the calls to read from API are identical
    api.get_namespaced_custom_object.assert_has_calls(read_calls)

    assert c1.spec.thisAttribute == "fourty three"


@patch("kubeobject.kubeobject.CustomObjectsApi")
def test_can_read_delete(patched_custom_objects_api: Mock):
    api = Mock(wraps=MockedCustomObjectsApi)
    patched_custom_objects_api.return_value = api

    C = KubeObject("example.com", "v1", "dummies")
    c = C.read("my-dummy-object", "default")

    api.get_namespaced_custom_object.assert_called_once()

    c.delete()
    api.delete_namespaced_custom_object.assert_called_once_with(
        group="example.com",
        version="v1",
        namespace="default",
        plural="dummies",
        name="my-dummy-object",
        body={},
    )


@patch("kubeobject.kubeobject.CustomObjectsApi")
def test_read_from_yaml_file(patched_custom_objects_api: Mock):
    api = Mock(wraps=MockedCustomObjectsApi)
    patched_custom_objects_api.return_value = api

    y = """
---
apiVersion: kubeobject.com/v1
kind: Dummy
metadata:
  name: my-dummy-object
spec:
  thisAttribute: "eighty one"
    """
    # we mock the file with a io.StringIO which acts like a file object.
    yaml_file = io.StringIO(y)

    C = KubeObject("example.com", "v1", "dummies")
    c = C.read_from_yaml_file(yaml_file)

    assert c.metadata.name == "my-dummy-object"
    assert c.spec.thisAttribute == "eighty one"
    assert c.apiVersion == "kubeobject.com/v1"
    assert c.kind == "Dummy"

    as_dict = yaml.safe_load(y)

    assert c.to_dict() == as_dict
