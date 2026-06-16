import unittest
from unittest.mock import patch

from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

# Capture real spans from the @TRACER.start_as_current_span decorator on
# assert_reaches_phase. OTel only honors set_tracer_provider once per process, so attach
# our in-memory exporter to whichever provider is active (installing a fresh one if the
# active provider is the no-op default that can't take processors).
_exporter = InMemorySpanExporter()
_provider = trace.get_tracer_provider()
if not hasattr(_provider, "add_span_processor"):
    _provider = TracerProvider()
    trace.set_tracer_provider(_provider)
_provider.add_span_processor(SimpleSpanProcessor(_exporter))

from kubetester.mongodb import MongoDB  # noqa: E402
from kubetester.opsmanager import MongoDBOpsManager  # noqa: E402
from kubetester.phase import Phase  # noqa: E402


def _make_mdb():
    # Build a MongoDB without invoking CustomObject __init__ machinery.
    return MongoDB.__new__(MongoDB)


def _last_span_attrs():
    spans = _exporter.get_finished_spans()
    return dict(spans[-1].attributes)


class TestAssertReachesPhaseAttributes(unittest.TestCase):
    def setUp(self):
        _exporter.clear()

    def test_failure_path_emits_fingerprint(self):
        mdb = _make_mdb()
        msg = (
            "Timeout (300) reached while waiting for MongoDB (mdb-rs)| status: Phase.Pending| "
            "message: StatefulSet not ready"
        )
        with patch.object(MongoDB, "wait_for", side_effect=Exception(msg)), patch.object(
            MongoDB, "get_status_phase", return_value=Phase.Pending
        ):
            with self.assertRaises(Exception):
                mdb.assert_reaches_phase(Phase.Running, timeout=1)

        attrs = _last_span_attrs()
        self.assertEqual(attrs["mck.outcome"], "failed")
        self.assertEqual(attrs["mck.desired_phase"], "Running")
        self.assertEqual(attrs["mck.observed_phase"], "Pending")
        self.assertEqual(
            attrs["mck.failure_pattern"],
            "Timeout (<n>) reached while waiting for MongoDB (<name>)| status: Phase.Pending| message: StatefulSet not ready",
        )
        self.assertNotIn("mck.failure_category", attrs)
        self.assertIn("mck.time_needed", attrs)

    def test_success_path_emits_reached_outcome(self):
        mdb = _make_mdb()
        with patch.object(MongoDB, "wait_for", return_value=True):
            mdb.assert_reaches_phase(Phase.Running, timeout=1)

        attrs = _last_span_attrs()
        self.assertEqual(attrs["mck.outcome"], "reached")
        self.assertEqual(attrs["mck.desired_phase"], "Running")
        self.assertIn("mck.time_needed", attrs)
        self.assertNotIn("mck.failure_pattern", attrs)
        self.assertNotIn("mck.failure_category", attrs)

    def test_om_status_failure_path_emits_fingerprint(self):
        # OmStatus/AppDbStatus/BackupStatus share StatusCommon.assert_reaches_phase.
        om = MongoDBOpsManager.__new__(MongoDBOpsManager)
        status = MongoDBOpsManager.OmStatus(om)
        msg = (
            'Got into Failed phase while waiting for Running! ("Status: 401 (Unauthorized), '
            'Detail: You are not authorized for this resource.")'
        )
        with patch.object(om, "wait_for", create=True, side_effect=Exception(msg)), patch.object(
            MongoDBOpsManager.OmStatus, "get_phase", return_value=Phase.Failed
        ):
            with self.assertRaises(Exception):
                status.assert_reaches_phase(Phase.Running, timeout=1)

        attrs = _last_span_attrs()
        self.assertEqual(attrs["mck.outcome"], "failed")
        self.assertEqual(attrs["mck.observed_phase"], "Failed")
        self.assertNotIn("mck.failure_category", attrs)
        self.assertIn("Status: 401", attrs["mck.failure_pattern"])


if __name__ == "__main__":
    unittest.main()
