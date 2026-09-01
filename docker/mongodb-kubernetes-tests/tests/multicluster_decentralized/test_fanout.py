"""Unit tests over the decentralized CR fan-out in MongoDBMulti. Pure mocks only: no cluster,
no live operator."""

from unittest import mock

import pytest
from kubernetes import client

from kubetester import decentralized_fanout
from kubetester.mongodb_multi import MongoDBMulti


@pytest.fixture(autouse=True)
def reset_registry():
    decentralized_fanout.disable()
    yield
    decentralized_fanout.disable()


def make_resource(name="mrs", namespace="mdb-ns", spec=None, annotations=None, labels=None, bound=False):
    resource = MongoDBMulti(name, namespace)
    resource.bound = bound
    resource.backing_obj = {
        "apiVersion": "mongodb.com/v1",
        "kind": "MongoDBMultiCluster",
        "metadata": {
            "name": name,
            "namespace": namespace,
            "annotations": annotations or {"team": "mck"},
            "labels": labels or {"app": "mrs"},
        },
        "spec": spec or {"clusterSpecList": [{"clusterName": "c1", "members": 3}]},
    }
    return resource


def patched_peer_apis(peer_apis_by_client):
    """Patches kubetester.mongodb_multi.client.CustomObjectsApi so that constructing it against a
    given peer ApiClient returns the mock pre-registered for that client."""

    def side_effect(api_client=None):
        return peer_apis_by_client[api_client]

    return mock.patch("kubetester.mongodb_multi.client.CustomObjectsApi", side_effect=side_effect)


def test_registry_disabled_update_touches_no_peer_client():
    resource = make_resource(bound=True)
    resource.api = mock.Mock()
    resource.api.patch_namespaced_custom_object.return_value = resource.backing_obj

    with mock.patch("kubetester.mongodb_multi.client.CustomObjectsApi") as api_cls:
        resource.update()

    api_cls.assert_not_called()
    resource.api.patch_namespaced_custom_object.assert_called_once()


def test_update_replaces_peer_copy_and_drops_removed_field():
    peer_client = object()
    decentralized_fanout.enable({"primary": object(), "peer1": peer_client}, primary="primary")

    # The source drops "extraField" that the peer's existing copy still has: this is the
    # scale-down case a merge-patch would silently no-op.
    resource = make_resource(spec={"clusterSpecList": [{"clusterName": "c1", "members": 2}]}, bound=True)
    resource.api = mock.Mock()
    resource.api.patch_namespaced_custom_object.return_value = resource.backing_obj

    peer_api = mock.Mock()
    peer_api.get_namespaced_custom_object.return_value = {
        "metadata": {"name": "mrs", "namespace": "mdb-ns", "resourceVersion": "999"},
        "spec": {"clusterSpecList": [{"clusterName": "c1", "members": 2}, {"clusterName": "c2", "members": 1}]},
    }

    with patched_peer_apis({peer_client: peer_api}):
        resource.update()

    peer_api.replace_namespaced_custom_object.assert_called_once()
    args, _ = peer_api.replace_namespaced_custom_object.call_args
    body = args[-1]
    assert body["spec"] == {"clusterSpecList": [{"clusterName": "c1", "members": 2}]}
    assert body["metadata"]["annotations"] == {"team": "mck"}
    assert body["metadata"]["labels"] == {"app": "mrs"}
    # The peer's own resourceVersion must survive untouched for the PUT.
    assert body["metadata"]["resourceVersion"] == "999"


def test_update_does_not_double_deliver_to_peers():
    """Regression guard: update() must not fan out twice. The base update() dispatches to
    create_or_update(), which itself calls self.patch() (bound resource) — already overridden
    with its own fan-out."""
    peer_client = object()
    decentralized_fanout.enable({"primary": object(), "peer1": peer_client}, primary="primary")

    resource = make_resource(bound=True)
    resource.api = mock.Mock()
    resource.api.patch_namespaced_custom_object.return_value = resource.backing_obj

    peer_api = mock.Mock()
    peer_api.get_namespaced_custom_object.return_value = {
        "metadata": {"name": "mrs", "namespace": "mdb-ns", "resourceVersion": "1"},
        "spec": {},
    }

    with patched_peer_apis({peer_client: peer_api}):
        resource.update()

    peer_api.replace_namespaced_custom_object.assert_called_once()


def test_create_seeds_absent_peer_with_sanitized_metadata():
    peer_client = object()
    decentralized_fanout.enable({"primary": object(), "peer1": peer_client}, primary="primary")

    resource = make_resource(bound=False)
    resource.backing_obj["metadata"].update(
        {"resourceVersion": "42", "uid": "abc-123", "creationTimestamp": "2026-01-01T00:00:00Z"}
    )
    resource.backing_obj["status"] = {"phase": "Running"}
    created = dict(resource.backing_obj)
    resource.api = mock.Mock()
    resource.api.create_namespaced_custom_object.return_value = created

    peer_api = mock.Mock()
    peer_api.get_namespaced_custom_object.side_effect = client.ApiException(status=404)

    with patched_peer_apis({peer_client: peer_api}):
        resource.create()

    peer_api.create_namespaced_custom_object.assert_called_once()
    args, kwargs = peer_api.create_namespaced_custom_object.call_args
    body = args[-1]
    assert "resourceVersion" not in body["metadata"]
    assert "uid" not in body["metadata"]
    assert "creationTimestamp" not in body["metadata"]
    assert "status" not in body
    assert kwargs.get("field_validation") == "Strict"


def test_one_failing_peer_does_not_block_the_other_and_names_the_failure():
    peer1, peer2 = object(), object()
    decentralized_fanout.enable({"primary": object(), "peer1": peer1, "peer2": peer2}, primary="primary")

    resource = make_resource(bound=True)
    resource.api = mock.Mock()
    resource.api.patch_namespaced_custom_object.return_value = resource.backing_obj

    failing_api = mock.Mock()
    failing_api.get_namespaced_custom_object.side_effect = Exception("peer1 unreachable")

    healthy_api = mock.Mock()
    healthy_api.get_namespaced_custom_object.side_effect = client.ApiException(status=404)

    with patched_peer_apis({peer1: failing_api, peer2: healthy_api}):
        with pytest.raises(Exception) as excinfo:
            resource.update()

    # peer2 was still attempted despite peer1 failing first.
    healthy_api.create_namespaced_custom_object.assert_called_once()
    assert "peer1" in str(excinfo.value)
    assert "peer1 unreachable" in str(excinfo.value)


def test_delete_removes_from_primary_and_peers_tolerating_404():
    primary_client, peer1, peer2 = object(), object(), object()
    decentralized_fanout.enable({"primary": primary_client, "peer1": peer1, "peer2": peer2}, primary="primary")

    resource = make_resource(bound=True)
    resource.api = mock.Mock()  # super().delete() succeeds on the primary.

    primary_api = mock.Mock()
    primary_api.get_namespaced_custom_object.side_effect = client.ApiException(status=404)

    peer1_api = mock.Mock()  # peer1's own delete is already gone (404), tolerated.
    peer1_api.delete_namespaced_custom_object.side_effect = client.ApiException(status=404)
    peer1_api.get_namespaced_custom_object.side_effect = client.ApiException(status=404)

    peer2_api = mock.Mock()  # delete succeeds outright.
    peer2_api.get_namespaced_custom_object.side_effect = client.ApiException(status=404)

    apis_by_client = {primary_client: primary_api, peer1: peer1_api, peer2: peer2_api}

    with mock.patch(
        "kubetester.mongodb_multi.client.CustomObjectsApi",
        side_effect=lambda api_client=None: apis_by_client[api_client],
    ):
        resource.delete()

    resource.api.delete_namespaced_custom_object.assert_called_once()
    peer2_api.delete_namespaced_custom_object.assert_called_once()


def test_delete_fails_naming_a_cluster_still_holding_the_object():
    peer1 = object()
    decentralized_fanout.enable({"primary": object(), "peer1": peer1}, primary="primary")

    resource = make_resource(bound=True)

    stuck_api = mock.Mock()
    stuck_api.get_namespaced_custom_object.return_value = {"metadata": {"name": "mrs"}}

    with mock.patch("kubetester.mongodb_multi.client.CustomObjectsApi", return_value=stuck_api):
        with pytest.raises(Exception) as excinfo:
            resource._wait_for_absence_everywhere(timeout=0.05, interval=0.01)

    assert "peer1" in str(excinfo.value)


def test_enable_rejects_primary_not_in_clients():
    with pytest.raises(ValueError):
        decentralized_fanout.enable({"a": object()}, primary="b")


# --- Secret/ConfigMap fan-out: the pre-provisioning contract for issuance-time materials ---


def patched_corev1(apis_by_client):
    """Patches kubetester.decentralized_fanout.client.CoreV1Api so that constructing it against a
    given ApiClient returns the mock pre-registered for that client."""

    def side_effect(api_client=None):
        return apis_by_client[api_client]

    return mock.patch("kubetester.decentralized_fanout.client.CoreV1Api", side_effect=side_effect)


def make_secret(data, type_="kubernetes.io/tls"):
    secret = mock.Mock()
    secret.data = data
    secret.type = type_
    return secret


def make_config_map(data, binary_data=None):
    config_map = mock.Mock()
    config_map.data = data
    config_map.binary_data = binary_data
    return config_map


def test_fan_out_secret_disabled_is_a_no_op():
    with mock.patch("kubetester.decentralized_fanout.client.CoreV1Api") as corev1_cls:
        decentralized_fanout.fan_out_secret("mdb-ns", "clustercert-mrs-cert")

    corev1_cls.assert_not_called()


def test_fan_out_secret_copies_data_and_type_to_every_peer():
    primary, peer1, peer2 = object(), object(), object()
    decentralized_fanout.enable({"primary": primary, "peer1": peer1, "peer2": peer2}, primary="primary")

    source_data = {"tls.crt": "Y3J0", "tls.key": "a2V5"}
    source_api = mock.Mock()
    source_api.read_namespaced_secret.return_value = make_secret(source_data)

    peer_apis = {}
    for peer in (peer1, peer2):
        peer_api = mock.Mock()
        peer_api.read_namespaced_secret.return_value = make_secret(source_data)
        peer_apis[peer] = peer_api

    with patched_corev1({primary: source_api, **peer_apis}):
        decentralized_fanout.fan_out_secret("mdb-ns", "clustercert-mrs-cert")

    source_api.read_namespaced_secret.assert_called_once_with("clustercert-mrs-cert", "mdb-ns")
    for peer_api in peer_apis.values():
        peer_api.create_namespaced_secret.assert_called_once()
        args, _ = peer_api.create_namespaced_secret.call_args
        body = args[-1]
        # The type must survive the copy: an Opaque copy of a kubernetes.io/tls source silently
        # disables TLS downstream (pem.ReadHashFromSecret falls back to "").
        assert body.type == "kubernetes.io/tls"
        assert body.data == source_data


def test_fan_out_secret_replaces_an_existing_copy():
    primary, peer1 = object(), object()
    decentralized_fanout.enable({"primary": primary, "peer1": peer1}, primary="primary")

    source_data = {"tls.crt": "Y3J0"}
    source_api = mock.Mock()
    source_api.read_namespaced_secret.return_value = make_secret(source_data)

    peer_api = mock.Mock()
    peer_api.create_namespaced_secret.side_effect = client.ApiException(status=409)
    peer_api.read_namespaced_secret.return_value = make_secret(source_data)

    with patched_corev1({primary: source_api, peer1: peer_api}):
        decentralized_fanout.fan_out_secret("mdb-ns", "clustercert-mrs-cert")

    peer_api.replace_namespaced_secret.assert_called_once()


def test_fan_out_secret_fails_loud_on_a_skewed_copy():
    """The byte-identical contract: a corrupted copy must fail the run naming the cluster —
    a silent skew would surface later as an AC cert path that is not a key in some member's
    mounted -pem secret."""
    primary, peer1 = object(), object()
    decentralized_fanout.enable({"primary": primary, "peer1": peer1}, primary="primary")

    source_api = mock.Mock()
    source_api.read_namespaced_secret.return_value = make_secret({"tls.crt": "Y3J0"})

    peer_api = mock.Mock()
    peer_api.read_namespaced_secret.return_value = make_secret({"tls.crt": "c2tld2Vk"})

    with patched_corev1({primary: source_api, peer1: peer_api}):
        with pytest.raises(AssertionError) as excinfo:
            decentralized_fanout.fan_out_secret("mdb-ns", "clustercert-mrs-cert")

    assert "peer1" in str(excinfo.value)
    assert "byte-identical" in str(excinfo.value)


def test_fan_out_secret_one_failing_peer_does_not_block_the_other_and_names_the_failure():
    primary, peer1, peer2 = object(), object(), object()
    decentralized_fanout.enable({"primary": primary, "peer1": peer1, "peer2": peer2}, primary="primary")

    source_data = {"tls.crt": "Y3J0"}
    source_api = mock.Mock()
    source_api.read_namespaced_secret.return_value = make_secret(source_data)

    failing_api = mock.Mock()
    failing_api.create_namespaced_secret.side_effect = Exception("peer1 unreachable")

    healthy_api = mock.Mock()
    healthy_api.read_namespaced_secret.return_value = make_secret(source_data)

    with patched_corev1({primary: source_api, peer1: failing_api, peer2: healthy_api}):
        with pytest.raises(Exception) as excinfo:
            decentralized_fanout.fan_out_secret("mdb-ns", "clustercert-mrs-cert")

    healthy_api.create_namespaced_secret.assert_called_once()
    assert "peer1" in str(excinfo.value)
    assert "peer1 unreachable" in str(excinfo.value)


def test_fan_out_config_map_copies_and_verifies():
    primary, peer1 = object(), object()
    decentralized_fanout.enable({"primary": primary, "peer1": peer1}, primary="primary")

    data = {"ca-pem": "CA", "mms-ca.crt": "CA"}
    source_api = mock.Mock()
    source_api.read_namespaced_config_map.return_value = make_config_map(data)

    peer_api = mock.Mock()
    peer_api.read_namespaced_config_map.return_value = make_config_map(data)

    with patched_corev1({primary: source_api, peer1: peer_api}):
        decentralized_fanout.fan_out_config_map("mdb-ns", "issuer-ca")

    peer_api.create_namespaced_config_map.assert_called_once()
    args, _ = peer_api.create_namespaced_config_map.call_args
    assert args[-1].data == data


def test_assert_copies_identical_is_order_insensitive_and_names_all_skewed_clusters():
    source = {"data": {"k": "v"}, "type": "Opaque"}

    decentralized_fanout.assert_copies_identical("Secret", "mdb-ns", "s", source, {"c1": dict(source)})

    with pytest.raises(AssertionError) as excinfo:
        decentralized_fanout.assert_copies_identical(
            "Secret", "mdb-ns", "s", source, {"c2": {"data": {"k": "x"}, "type": "Opaque"}, "c1": dict(source)}
        )
    assert "c2" in str(excinfo.value)
    assert "c1" not in str(excinfo.value)
