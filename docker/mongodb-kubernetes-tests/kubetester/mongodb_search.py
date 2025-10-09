from __future__ import annotations

import time

from kubeobject import CustomObject
from kubetester.mongodb import MongoDB
from kubetester.mongodb_utils_state import in_desired_state
from kubetester.phase import Phase
from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


class MongoDBSearch(MongoDB, CustomObject):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbsearch",
            "kind": "MongoDBSearch",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBSearch, self).__init__(*args, **with_defaults)

    @classmethod
    def from_yaml(cls, yaml_file, name=None, namespace=None, with_mdb_version_from_env=False) -> MongoDBSearch:
        resource = super().from_yaml(yaml_file=yaml_file, name=name, namespace=namespace)
        return resource

    def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False):
        start_time = time.time()

        self.wait_for(
            lambda s: in_desired_state(
                current_state=self.get_status_phase(),
                desired_state=phase,
                current_generation=self.get_generation(),
                observed_generation=self.get_status_observed_generation(),
                current_message=self.get_status_message(),
                msg_regexp=msg_regexp,
                ignore_errors=ignore_errors,
            ),
            timeout,
            should_raise=True,
        )

        end_time = time.time()
        span = trace.get_current_span()
        span.set_attribute("mck.resource", self.__class__.__name__)
        span.set_attribute("mck.action", "assert_phase")
        span.set_attribute("mck.desired_phase", phase.name)
        span.set_attribute("mck.time_needed", end_time - start_time)
        logger.debug(
            f"Reaching phase {phase.name} for resource {self.__class__.__name__} took {end_time - start_time}s"
        )
