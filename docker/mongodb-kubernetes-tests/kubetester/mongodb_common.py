from __future__ import annotations

import time

from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


class MongoDBCommon:
    @TRACER.start_as_current_span("wait_for")
    def wait_for(self, fn, timeout=None, should_raise=True):
        if timeout is None:
            timeout = 600
        initial_timeout = timeout

        wait = 3
        while timeout > 0:
            try:
                self.reload()
            except Exception as e:
                print(f"Caught error: {e} while waiting for {fn.__name__}")
                pass
            if fn(self):
                return True
            timeout -= wait
            time.sleep(wait)

        if should_raise:
            raise Exception("Timeout ({}) reached while waiting for {}".format(initial_timeout, self))

    def get_generation(self) -> int:
        return self.backing_obj["metadata"]["generation"]
