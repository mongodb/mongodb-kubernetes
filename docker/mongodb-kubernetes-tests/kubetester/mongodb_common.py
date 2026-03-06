from __future__ import annotations

import time

from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


class MongoDBCommon:
    @TRACER.start_as_current_span("wait_for")
    def wait_for(self, fn, timeout=None, should_raise=True, persist_for=1):
        """
        Waits for the given function `fn` to return True, retrying until the timeout is reached.
        If persist_for > 1, the function must return True for that many consecutive checks.
        Optionally raises an exception if the condition is not met within the timeout.

        Args:
            fn: A callable that returns a boolean.
            timeout: Maximum time to wait in seconds (default: 600).
            should_raise: If True, raises an Exception on timeout (default: True).
            persist_for: Number of consecutive successful checks required (default: 1).
        Returns:
            True if the condition is met within the timeout, otherwise raises Exception if `should_raise` is True.
        """
        if timeout is None:
            timeout = 600
        initial_timeout = timeout

        wait = 3
        retries = 0
        while timeout > 0:
            try:
                self.reload()
            except Exception as e:
                print(f"Caught error: {e} while waiting for {fn.__name__}")
                pass
            if fn(self):
                retries += 1
                if retries == persist_for:
                    return True
            else:
                retries = 0

            timeout -= wait
            time.sleep(wait)

        if should_raise:
            raise Exception("Timeout ({}) reached while waiting for {}".format(initial_timeout, self))

    def get_generation(self) -> int:
        return self.backing_obj["metadata"]["generation"]
