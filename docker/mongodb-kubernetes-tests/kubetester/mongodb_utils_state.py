from __future__ import annotations

import re
from typing import Optional, Tuple

from kubetester.phase import Phase
from opentelemetry import trace
from tests import test_logger

TRACER = trace.get_tracer("evergreen-agent")
logger = test_logger.get_test_logger(__name__)


@TRACER.start_as_current_span("in_desired_state")
def in_desired_state(
    current_state: Phase,
    desired_state: Phase,
    current_generation: int,
    observed_generation: int,
    current_message: str,
    msg_regexp: Optional[str] = None,
    ignore_errors=False,
    intermediate_events: Tuple = (),
) -> bool:
    """Returns true if the current_state is equal to desired state, fails fast if got into Failed error.
    Optionally checks if the message matches the specified regexp expression"""
    if current_state is None:
        return False

    if current_generation != observed_generation:
        # We shouldn't check the status further if the Operator hasn't started working on the new spec yet
        return False

    if current_state == Phase.Failed and not desired_state == Phase.Failed and not ignore_errors:
        found = False
        for event in intermediate_events:
            if event in current_message:
                found = True
                logger.debug(
                    f"Found intermediate event in failure: {event} in {current_message}. Skipping the failure state"
                )

        if not found:
            raise AssertionError(f'Got into Failed phase while waiting for Running! ("{current_message}")')

    is_in_desired_state = current_state == desired_state
    if msg_regexp is not None:
        print("msg_regexp: " + str(msg_regexp))
        regexp = re.compile(msg_regexp)
        is_in_desired_state = is_in_desired_state and current_message is not None and regexp.match(current_message)

    return is_in_desired_state
