import unittest
from unittest.mock import MagicMock, patch

from pymongo.errors import OperationFailure
from tests.authentication.sharded_cluster_x509_to_scram_transition import (
    _CREATE_USER_FAILED,
    _CREATE_USER_NOT_PRIMARY,
    _CREATE_USER_OK,
    _classify_create_user_result,
    _create_automation_agent_user,
)

MODULE = "tests.authentication.sharded_cluster_x509_to_scram_transition"


class TestClassifyCreateUserResult(unittest.TestCase):
    """Pure-function tests for the createUser outcome classifier."""

    def test_success_when_returncode_zero(self):
        self.assertEqual(_classify_create_user_result(0, "", ""), _CREATE_USER_OK)

    def test_already_exists_stops(self):
        self.assertEqual(_classify_create_user_result(1, "", "User already exists"), _CREATE_USER_OK)

    def test_already_exists_case_insensitive(self):
        self.assertEqual(
            _classify_create_user_result(1, "", "USER ALREADY EXISTS"),
            _CREATE_USER_OK,
        )

    def test_not_primary_retries_next_pod(self):
        self.assertEqual(
            _classify_create_user_result(1, "", "MongoServerError: not primary"),
            _CREATE_USER_NOT_PRIMARY,
        )

    def test_notwritableprimary_retries_next_pod(self):
        self.assertEqual(
            _classify_create_user_result(1, "", "NotWritablePrimary"),
            _CREATE_USER_NOT_PRIMARY,
        )

    def test_requires_authentication_is_not_success(self):
        self.assertEqual(
            _classify_create_user_result(1, "", "requires authentication"),
            _CREATE_USER_FAILED,
        )

    def test_arbitrary_error_is_not_success(self):
        self.assertEqual(
            _classify_create_user_result(1, "", "connection refused"),
            _CREATE_USER_FAILED,
        )


class TestCreateAutomationAgentUserFailover(unittest.TestCase):
    """Tests for config-server pod failover and error propagation."""

    @patch(f"{MODULE}.subprocess.run")
    @patch(f"{MODULE}.MongoClient")
    @patch(f"{MODULE}.read_secret")
    def test_advances_to_next_pod_on_not_primary_and_raises_when_all_fail(
        self, mock_read_secret, mock_mongo_client, mock_subprocess_run
    ):
        mock_read_secret.return_value = {"automation-agent-password": "pw"}

        mock_client = MagicMock()
        mock_client.admin.command.side_effect = OperationFailure("auth failed", code=18)
        mock_mongo_client.return_value = mock_client

        mock_subprocess_run.side_effect = [
            MagicMock(returncode=1, stdout="", stderr="not primary"),
            MagicMock(returncode=1, stdout="", stderr="requires authentication"),
        ]

        with self.assertRaises(RuntimeError) as ctx:
            _create_automation_agent_user("ns", "res", "/ca")

        self.assertIn("res-config-0", str(ctx.exception))
        self.assertIn("res-config-1", str(ctx.exception))
        self.assertEqual(mock_subprocess_run.call_count, 2)

        # Raw mongosh stderr must not leak into the exception: create_user_js
        # contains the password and mongosh may echo the eval'd script on error.
        self.assertNotIn("requires authentication", str(ctx.exception))

        # Assert the subprocess commands targeted config-0 first, config-1 second.
        # cmd layout: ["kubectl", "exec", "-n", namespace, pod, "-c", ...]  -> pod at index 4
        first_pod = mock_subprocess_run.call_args_list[0].args[0][4]
        second_pod = mock_subprocess_run.call_args_list[1].args[0][4]
        self.assertEqual(first_pod, "res-config-0")
        self.assertEqual(second_pod, "res-config-1")

    @patch(f"{MODULE}.subprocess.run")
    @patch(f"{MODULE}.MongoClient")
    @patch(f"{MODULE}.read_secret")
    def test_stops_after_success_on_first_pod(self, mock_read_secret, mock_mongo_client, mock_subprocess_run):
        mock_read_secret.return_value = {"automation-agent-password": "pw"}

        mock_client = MagicMock()
        mock_client.admin.command.side_effect = OperationFailure("auth failed", code=18)
        mock_mongo_client.return_value = mock_client

        mock_subprocess_run.return_value = MagicMock(returncode=0, stdout="", stderr="")

        _create_automation_agent_user("ns", "res", "/ca")
        self.assertEqual(mock_subprocess_run.call_count, 1)
        # MongoClient must be closed even when the ping probe raised.
        mock_client.close.assert_called_once()

    @patch(f"{MODULE}.subprocess.run")
    @patch(f"{MODULE}.MongoClient")
    @patch(f"{MODULE}.read_secret")
    def test_stops_when_already_exists_on_second_pod(self, mock_read_secret, mock_mongo_client, mock_subprocess_run):
        mock_read_secret.return_value = {"automation-agent-password": "pw"}

        mock_client = MagicMock()
        mock_client.admin.command.side_effect = OperationFailure("auth failed", code=18)
        mock_mongo_client.return_value = mock_client

        mock_subprocess_run.side_effect = [
            MagicMock(returncode=1, stdout="", stderr="not primary"),
            MagicMock(returncode=1, stdout="", stderr="already exists"),
        ]

        _create_automation_agent_user("ns", "res", "/ca")
        self.assertEqual(mock_subprocess_run.call_count, 2)

    @patch(f"{MODULE}.MongoClient")
    @patch(f"{MODULE}.read_secret")
    def test_probe_uses_scram_sha_256(self, mock_read_secret, mock_mongo_client):
        mock_read_secret.return_value = {"automation-agent-password": "pw"}

        mock_client = MagicMock()
        mock_client.admin.command.return_value = {"ok": 1}
        mock_mongo_client.return_value = mock_client

        _create_automation_agent_user("ns", "res", "/ca")

        mock_mongo_client.assert_called_once()
        _, kwargs = mock_mongo_client.call_args
        self.assertEqual(kwargs.get("authMechanism"), "SCRAM-SHA-256")
        # MongoClient must be closed on the success path too.
        mock_client.close.assert_called_once()
