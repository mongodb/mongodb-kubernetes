import os


def is_cloud_qa() -> bool:
    return os.getenv("ops_manager_version", "cloud_qa") == "cloud_qa"
