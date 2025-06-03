from __future__ import annotations

from typing import Optional

from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager


def generic_replicaset(
    namespace: str,
    version: str,
    name: Optional[str] = None,
    ops_manager: Optional[MongoDBOpsManager] = None,
) -> MongoDB:
    if name is None:
        name = KubernetesTester.random_k8s_name("rs-")

    rs = MongoDB(namespace=namespace, name=name)
    rs.configure(ops_manager)
    rs["spec"] = {
        "members": 3,
        "type": "ReplicaSet",
        "persistent": False,
        "version": version,
    }

    return rs
