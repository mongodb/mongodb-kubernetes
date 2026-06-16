import logging
from typing import Any, Optional

import pytest
from _pytest.main import Session
from _pytest.nodes import Node
from _pytest.reports import TestReport
from _pytest.runner import CallInfo
from opentelemetry import trace
from opentelemetry.sdk.trace import ReadableSpan, Span, SpanProcessor, TracerProvider
from pytest_opentelemetry.instrumentation import OpenTelemetryPlugin


# Fixture spans that carry no failure signal and are generated in large numbers.
# These are pure configuration reads or trivial setup/teardown steps whose timing
# and outcome add no value for debugging. DROP_SPAN=True causes the Honeycomb
# ingestion pipeline to discard them before storage.
# ~7-8M spans/7d reduced by keeping this list tight.
_DROP_FIXTURE_SPANS = frozenset(
    {
        # Python event-loop setup — always sub-ms, never fails
        "event_loop_policy setup",
        "event_loop_policy teardown",
        # Kubernetes namespace name — just returns a string constant
        "namespace setup",
        "namespace teardown",
        # Cluster topology helpers — read-only accessors, no resource creation
        "central_cluster_name setup",
        "central_cluster_name teardown",
        "member_cluster_names setup",
        "member_cluster_names teardown",
        "central_cluster_client setup",
        "central_cluster_client teardown",
        "member_cluster_clients setup",
        "member_cluster_clients teardown",
        # Generic helper fixture (returns tester utility, no I/O)
        "helper setup",
        "helper teardown",
        # Version string accessor
        "custom_mdb_version setup",
        "custom_mdb_version teardown",
        # Multi-cluster operator config object — read-only dict wrapper
        "multi_cluster_operator_installation_config setup",
        "multi_cluster_operator_installation_config teardown",
        # Reads a ConfigMap and returns it as a dict — no resource creation
        "operator_installation_config setup",
        "operator_installation_config teardown",
    }
)


class PrefixProcessor(SpanProcessor):
    def on_start(self, span: Span, parent_context=None):
        # Create a new dictionary for updated attributes, span.attribute is immutable
        prefixed_attributes = EnhancedOpenTelemetryPlugin._prefix_attributes(span.attributes)
        span.set_attributes(prefixed_attributes)

        if span.name in _DROP_FIXTURE_SPANS:
            span.set_attribute("DROP_SPAN", True)

    def on_end(self, span: ReadableSpan):
        pass


#  We are using a custom OpenTelemetryPlugin to ensure we are able to add more
#  important failure information, outcome etc.
class EnhancedOpenTelemetryPlugin(OpenTelemetryPlugin):
    # This ensures that our pytest finish runs first before the plugins and we can attach spans before
    # they are getting flushed.
    def pytest_sessionfinish(self, session: Session, exitstatus: Optional[int] = None) -> None:
        # Add the exit status as an attribute if available
        self.session_span.set_attribute("mck.pytest.overall_exit_status", int(session.exitstatus))

        # Call the parent implementation
        super().pytest_sessionfinish(session)

    @staticmethod
    def pytest_exception_interact(
        node: Node,
        call: CallInfo[Any],
        report: TestReport,
    ) -> None:
        current_span = trace.get_current_span()
        if not isinstance(current_span, ReadableSpan):
            return
        prefixed_attributes = EnhancedOpenTelemetryPlugin._prefix_attributes(current_span.attributes)
        prefixed_attributes["mck.pytest.error_details"] = str(report.longrepr)
        current_span.set_attributes(prefixed_attributes)

        OpenTelemetryPlugin.pytest_exception_interact(node, call, report)

    @staticmethod
    def _prefix_attributes(attributes):
        """Add 'mck.' prefix to attribute keys that don't already have it."""
        prefixed_attributes = {}
        for k, v in attributes.items():
            if not k.startswith("mck."):
                prefixed_attributes[f"mck.{k}"] = v
            else:
                prefixed_attributes[k] = v
        return prefixed_attributes

    @pytest.hookimpl(hookwrapper=True)
    def pytest_runtest_makereport(self, item, call):
        outcome = yield
        report = outcome.get_result()
        current_span = trace.get_current_span()
        if not current_span:
            return

        attributes = self._attributes_from_item(item)
        prefixed_attributes = self._prefix_attributes(attributes)
        current_span.set_attributes(prefixed_attributes)
        current_span.set_attribute(f"mck.pytest.outcome.{call.when}", report.outcome)


def _configure_telemetry():
    # Get the existing tracer provider that was set up by pytest-opentelemetry
    tracer_provider = trace.get_tracer_provider()

    if isinstance(tracer_provider, TracerProvider):
        prefix_processor = PrefixProcessor()
        tracer_provider.add_span_processor(prefix_processor)


# Remove the OpenTelemetryPlugin from the list and replace it with our custom generated one.
# That's why we run our pytest last.
@pytest.hookimpl(trylast=True)
def pytest_configure(config):
    # Suppress the OpenTelemetry SDK warnings caused by swapping these plugins
    logging.getLogger("opentelemetry").setLevel(logging.ERROR)

    # Remove the default plugin if already registered
    for i, plugin_instance in enumerate(config.pluginmanager.get_plugins()):
        if isinstance(plugin_instance, OpenTelemetryPlugin):
            config.pluginmanager.unregister(plugin=plugin_instance)
            break

    config.pluginmanager.register(EnhancedOpenTelemetryPlugin())


def pytest_sessionstart():
    _configure_telemetry()
