import pytest
import os


def running_cloud_manager():
    "Determines if the current test is running against Cloud Manager"
    return os.getenv("OM_HOST", "") == "https://cloud-qa.mongodb.com"


skip_if_cloud_manager = pytest.mark.skipif(running_cloud_manager(), reason="Do not run in Cloud Manager")
