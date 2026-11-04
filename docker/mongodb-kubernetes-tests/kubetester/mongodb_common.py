from __future__ import annotations

import time

from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


class MongoDBCommon:
    backing_obj: dict

    @TRACER.start_as_current_span("wait_for")
    def wait_for(self, fn, timeout=None, should_raise=True, persist_for=1, name=None):
        """
        Waits for the given function `fn` to return True, retrying until the timeout is reached.
        If persist_for > 1, the function must return True for that many consecutive checks.
        Optionally raises an exception if the condition is not met within the timeout.

        Args:
            fn: A callable that returns a boolean.
            timeout: Maximum time to wait in seconds (default: 600).
            should_raise: If True, raises an Exception on timeout (default: True).
            persist_for: Number of consecutive successful checks required (default: 1).
            name: Wait name recorded as `mck.wait.name` (default: `fn.__name__`). Useful for
                lambdas, whose `__name__` is `<lambda>`.
        Returns:
            True if the condition is met within the timeout, otherwise raises Exception if `should_raise` is True.
        """
        if timeout is None:
            timeout = 600
        initial_timeout = timeout
        wait_name = name or getattr(fn, "__name__", type(fn).__name__)

        span = trace.get_current_span()
        span.set_attribute("mck.wait.name", wait_name)
        span.set_attribute("mck.wait.timeout_s", initial_timeout)

        wait = 3
        retries = 0
        attempts = 0
        while timeout > 0:
            attempts += 1
            try:
                self.reload()
            except Exception as e:
                print(f"Caught error: {e} while waiting for {wait_name}")
                pass
            if fn(self):
                retries += 1
                if retries == persist_for:
                    phase, message = self._observed_status()
                    self._record_wait(span, attempts, phase, message)
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
        attempts += 1
        if fn(self):
            phase, message = self._observed_status()
            self._record_wait(span, attempts, phase, message)
            return True

        phase, message = self._observed_status()
        error = (
            f"Timeout ({initial_timeout}) reached while waiting for {self} after {attempts} attempt(s) "
            f"(last observed phase: {phase}, message: {message})"
        )
        self._record_wait(span, attempts, phase, message, error)
        if should_raise:
            raise Exception(error)

    def _observed_status(self):
        """Returns the status phase and message of the object, or (None, None) when it has no status."""
        phase = None
        message = None
        try:
            if hasattr(self, "get_status_phase"):
                phase = self.get_status_phase()
                phase = getattr(phase, "name", phase)
            if hasattr(self, "get_status_message"):
                message = self.get_status_message()
        except Exception as e:
            logger.debug(f"Could not read status while waiting: {e}")
        return phase, message

    @staticmethod
    def _record_wait(span, attempts, phase=None, message=None, error=None):
        """Records a wait outcome on the current span as `mck.wait.*` attributes."""
        span.set_attribute("mck.wait.attempts", attempts)
        if phase is not None:
            span.set_attribute("mck.wait.last_observed.phase", str(phase))
        if message is not None:
            span.set_attribute("mck.wait.last_observed.message", str(message))
        if error is not None:
            span.set_attribute("mck.wait.error", str(error))

    def get_generation(self) -> int:
        return self.backing_obj["metadata"]["generation"]
