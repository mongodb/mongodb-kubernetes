"""Unit tests for the cross-cluster Secret replicator.

These tests use mocked kubernetes clients (no live cluster needed).
"""
from unittest.mock import MagicMock

import pytest
from kubernetes.client.exceptions import ApiException

from tests.common.multicluster_search.secret_replicator import replicate_secret


def _mock_central_client(secret_data: dict[str, bytes]) -> MagicMock:
    client = MagicMock()
    secret = MagicMock()
    secret.data = {k: v for k, v in secret_data.items()}
    secret.type = "Opaque"
    secret.metadata.labels = {"app": "mdb-search"}
    client.read_namespaced_secret.return_value = secret
    return client


def test_replicate_creates_secret_in_each_member():
    central = _mock_central_client({"tls.crt": b"PEMDATA", "tls.key": b"KEYDATA"})
    member_a = MagicMock()
    member_b = MagicMock()
    member_a.read_namespaced_secret.side_effect = ApiException(status=404)
    member_b.read_namespaced_secret.side_effect = ApiException(status=404)

    replicate_secret(
        secret_name="my-tls-cert",
        namespace="ns",
        central_client=central,
        member_clients={"cluster-a": member_a, "cluster-b": member_b},
    )

    assert member_a.create_namespaced_secret.called
    assert member_b.create_namespaced_secret.called
    a_args = member_a.create_namespaced_secret.call_args
    assert a_args.kwargs["namespace"] == "ns"
    assert a_args.kwargs["body"].data == {"tls.crt": b"PEMDATA", "tls.key": b"KEYDATA"}


def test_replicate_updates_existing_secret():
    central = _mock_central_client({"tls.crt": b"NEWPEM"})
    member_a = MagicMock()
    existing = MagicMock()
    existing.data = {"tls.crt": b"OLDPEM"}
    member_a.read_namespaced_secret.return_value = existing

    replicate_secret(
        secret_name="my-tls-cert",
        namespace="ns",
        central_client=central,
        member_clients={"cluster-a": member_a},
    )

    assert member_a.patch_namespaced_secret.called
    patch_args = member_a.patch_namespaced_secret.call_args
    assert patch_args.kwargs["body"].data == {"tls.crt": b"NEWPEM"}


def test_replicate_idempotent_when_data_matches():
    central = _mock_central_client({"tls.crt": b"PEMDATA"})
    member_a = MagicMock()
    existing = MagicMock()
    existing.data = {"tls.crt": b"PEMDATA"}
    member_a.read_namespaced_secret.return_value = existing

    replicate_secret(
        secret_name="my-tls-cert",
        namespace="ns",
        central_client=central,
        member_clients={"cluster-a": member_a},
    )

    assert not member_a.create_namespaced_secret.called
    assert not member_a.patch_namespaced_secret.called
