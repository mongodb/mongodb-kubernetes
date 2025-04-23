from __future__ import annotations

import time

from kubeobject import CustomObject
from kubetester.mongodb import MongoDB, Phase, in_desired_state
from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


class MongoDBCommunity(MongoDB, CustomObject):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbcommunity",
            "kind": "MongoDBCommunity",
            "group": "mongodbcommunity.mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBCommunity, self).__init__(*args, **with_defaults)

    @classmethod
    def from_yaml(cls, yaml_file, name=None, namespace=None, with_mdb_version_from_env=False) -> MongoDBCommunity:
        resource = super().from_yaml(
            yaml_file=yaml_file, name=name, namespace=namespace, with_mdb_version_from_env=False
        )
        return resource

    def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False):
        # intermediate_events is a tuple
        intermediate_events = ("updating the status",)

        start_time = time.time()

        self.wait_for(
            lambda s: in_desired_state(
                current_state=self.get_status_phase(),
                desired_state=phase,
                # TODO: MCK we don't have "observedGeneration" in MongoDBCommunity status
                current_generation=1,
                observed_generation=1,
                current_message=self.get_status_message(),
                msg_regexp=msg_regexp,
                ignore_errors=ignore_errors,
                intermediate_events=intermediate_events,
            ),
            timeout,
            should_raise=True,
        )

        end_time = time.time()
        span = trace.get_current_span()
        span.set_attribute("meko_resource", self.__class__.__name__)
        span.set_attribute("meko_action", "assert_phase")
        span.set_attribute("meko_desired_phase", phase.name)
        span.set_attribute("meko_time_needed", end_time - start_time)
        logger.debug(
            f"Reaching phase {phase.name} for resource {self.__class__.__name__} took {end_time - start_time}s"
        )
