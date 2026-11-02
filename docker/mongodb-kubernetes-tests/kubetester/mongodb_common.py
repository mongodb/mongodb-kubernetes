from __future__ import annotations

import time

from kubetester.polling import RETRYABLE_READ_ERRORS, is_transient_read_error, poll_until
from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


class MongoDBCommon:
    backing_obj: dict

    @TRACER.start_as_current_span("wait_for")
    def wait_for(
        self, fn, timeout=None, should_raise=True, persist_for=1, *, use_shared_poller=False, description=None
    ):
        """
        Waits for the given function `fn` to return True, retrying until the timeout is reached.
        If persist_for > 1, the function must return True for that many consecutive checks.
        Optionally raises an exception if the condition is not met within the timeout.

        Args:
            fn: A callable that returns a boolean.
            timeout: Maximum time to wait in seconds (default: 600).
            should_raise: If True, raises an Exception on timeout (default: True).
            persist_for: Number of consecutive successful checks required (default: 1).
            use_shared_poller: Opt in to elapsed-time deadlines and fresh reads.
            description: Optional name for the condition in timeout diagnostics.
        Returns:
            True on success; raises on timeout unless `should_raise` is False, in which case it returns None.
        """
        if timeout is None:
            timeout = 600
        if use_shared_poller:

            def check(deadline):
                self.reload(request_timeout=deadline.remaining())
                return fn(self)

            return poll_until(
                check,
                timeout=timeout,
                interval=3,
                description=description or f"{self}: {fn.__name__}",
                retry_on=RETRYABLE_READ_ERRORS,
                retry_if=is_transient_read_error,
                persist_for=persist_for,
                should_raise=should_raise,
            )

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

        # ponytail: one last check -- condition may have become true during the final sleep
        try:
            self.reload()
        except Exception:
            pass
        if fn(self):
            return True

        if should_raise:
            raise Exception("Timeout ({}) reached while waiting for {}".format(initial_timeout, self))

    def get_generation(self) -> int:
        return self.backing_obj["metadata"]["generation"]
