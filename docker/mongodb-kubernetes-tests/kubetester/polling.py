import logging
import math
import time
from collections.abc import Callable

import requests
from kubernetes.client.rest import ApiException
from urllib3 import exceptions as urllib3_errors

logger = logging.getLogger(__name__)

RETRYABLE_READ_ERRORS = (
    ApiException,
    requests.exceptions.RequestException,
    urllib3_errors.HTTPError,
    TimeoutError,
    ConnectionError,
)
RETRYABLE_HTTP_STATUSES = frozenset({429, 502, 503, 504})


def is_transient_read_error(error: Exception) -> bool:
    if isinstance(error, ApiException):
        return error.status in RETRYABLE_HTTP_STATUSES
    if isinstance(error, requests.exceptions.HTTPError):
        return error.response is not None and error.response.status_code in RETRYABLE_HTTP_STATUSES
    if isinstance(error, (requests.exceptions.SSLError, requests.exceptions.ProxyError, urllib3_errors.SSLError)):
        return False
    if isinstance(error, urllib3_errors.MaxRetryError):
        # urllib3 discards the response on exhaustion and records its status in ResponseError.
        if isinstance(error.reason, urllib3_errors.ResponseError):
            return any(
                str(error.reason) == urllib3_errors.ResponseError.SPECIFIC_ERROR.format(status_code=status)
                for status in RETRYABLE_HTTP_STATUSES
            )
        return isinstance(error.reason, Exception) and is_transient_read_error(error.reason)
    return isinstance(
        error,
        (
            requests.exceptions.Timeout,
            requests.exceptions.ConnectionError,
            urllib3_errors.ConnectTimeoutError,
            urllib3_errors.ReadTimeoutError,
            urllib3_errors.ProtocolError,
            TimeoutError,
            ConnectionError,
        ),
    )


class PollTimeoutError(AssertionError):
    pass


class DeadlineExpired(TimeoutError):
    pass


class Deadline:
    def __init__(self, expires_at: float):
        self.expires_at = expires_at

    def remaining(self) -> float:
        remaining = self.expires_at - time.monotonic()
        if remaining <= 0:
            raise DeadlineExpired
        return remaining


def _parse_result(result: bool | tuple[bool, str]) -> tuple[bool, str]:
    if isinstance(result, bool):
        return result, "ready" if result else "not ready"
    if isinstance(result, tuple) and len(result) == 2 and isinstance(result[0], bool) and isinstance(result[1], str):
        return result
    raise TypeError("poll check must return a bool or a (bool, str) tuple")


def poll_until(
    check: Callable[[Deadline], bool | tuple[bool, str]],
    *,
    timeout: float,
    interval: float,
    description: str,
    retry_on: tuple[type[Exception], ...] = (),
    retry_if: Callable[[Exception], bool] | None = None,
    persist_for: int = 1,
    should_raise: bool = True,
) -> bool | None:
    """Poll a condition until it succeeds or its elapsed-time deadline expires.

    The check must use the supplied deadline to bound its own I/O.
    """
    if not math.isfinite(timeout) or timeout <= 0:
        raise ValueError("timeout must be a positive, finite number")
    if not math.isfinite(interval) or interval <= 0:
        raise ValueError("interval must be a positive, finite number")
    if persist_for < 1:
        raise ValueError("persist_for must be at least one")

    start = time.monotonic()
    deadline = Deadline(start + timeout)
    attempts = 0
    consecutive = 0
    last_observation = "no successful observations"
    last_error = None

    while True:
        try:
            deadline.remaining()
            attempts += 1
            result = check(deadline)
        except DeadlineExpired:
            break
        except retry_on as exc:
            if retry_if is not None and not retry_if(exc):
                raise
            consecutive = 0
            status = getattr(exc, "status", None)
            if status is None:
                status = getattr(getattr(exc, "response", None), "status_code", None)
            last_error = type(exc).__name__ + (f" (HTTP {status})" if status else "")
            logger.warning("Transient read error while waiting for %s: %s", description, last_error)
        else:
            ready, last_observation = _parse_result(result)
            consecutive = consecutive + 1 if ready else 0
            if consecutive >= persist_for:
                try:
                    deadline.remaining()
                except DeadlineExpired:
                    break
                return True

        try:
            time.sleep(min(interval, deadline.remaining()))
        except DeadlineExpired:
            break

    message = (
        f"Timed out waiting for {description} after {time.monotonic() - start:.1f}s and {attempts} attempt(s); "
        f"last observation: {last_observation}"
    )
    if last_error:
        message += f"; last transient read error: {last_error}"
    if should_raise:
        raise PollTimeoutError(message)
    logger.warning(message)
    return None
