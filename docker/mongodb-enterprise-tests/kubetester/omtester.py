import pytest
import os

from .kubetester import get_env_var_or_fail

def running_cloud_manager():
    "Determines if the current test is running against Cloud Manager"
    return get_env_var_or_fail("OM_HOST") == "https://cloud-qa.mongodb.com"


skip_if_cloud_manager = pytest.mark.skipif(running_cloud_manager(), reason="Do not run in Cloud Manager")
