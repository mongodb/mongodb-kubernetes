#!/usr/bin/env python3

import os
from typing import Optional

from pytest import fixture


@fixture(scope="module")
def custom_version() -> Optional[str]:
    """Returns a CUSTOM_OM_VERSION for OM, or None if the variable is not set."""
    return os.getenv("CUSTOM_OM_VERSION")
