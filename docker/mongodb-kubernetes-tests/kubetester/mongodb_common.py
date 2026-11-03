from __future__ import annotations

from kubetester.polling import poll_until
from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


class MongoDBCommon:
    backing_obj: dict

    @TRACER.start_as_current_span("wait_for")
    def wait_for(self, fn, timeout=None, should_raise=True, persist_for=1, *, description=None):
        """
        Waits for the given function `fn` to return True, reloading before every check.
        If persist_for > 1, the function must return True for that many consecutive checks.
        Optionally raises an exception if the condition is not met within the timeout.

        Args:
            fn: A callable that returns a boolean.
            timeout: Maximum time to wait in seconds (default: 600).
            should_raise: If True, raises an Exception on timeout (default: True).
            persist_for: Number of consecutive successful checks required (default: 1).
            description: Optional name for the condition in timeout diagnostics.
        Returns:
            True on success; raises on timeout unless `should_raise` is False, in which case it returns None.
        """

        def check(deadline):
            self.reload(request_timeout=deadline.remaining())
            return fn(self)

        return poll_until(
            check,
            timeout=600 if timeout is None else timeout,
            interval=3,
            description=description or f"{self}: {fn.__name__}",
            persist_for=persist_for,
            should_raise=should_raise,
        )

    def get_generation(self) -> int:
        return self.backing_obj["metadata"]["generation"]
