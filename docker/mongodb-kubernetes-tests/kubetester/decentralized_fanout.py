"""Process-global registry that toggles CR fan-out on MongoDBMulti. It exists because in
decentralized operator mode each cluster's member-spec fence only ever looks at its OWN copy of
the CR, while legacy e2e tests write the CR once on a single (central) cluster; the
DECENTRALIZED_E2E operator fixture enables this registry so those writes replicate everywhere.
"""

from __future__ import annotations

from typing import Dict, Optional

from kubernetes import client

_clients: Dict[str, client.ApiClient] = {}
_primary: Optional[str] = None


def enable(clients: Dict[str, client.ApiClient], primary: str) -> None:
    if primary not in clients:
        raise ValueError(f"primary cluster {primary!r} not among clients {sorted(clients)}")

    global _clients, _primary
    _clients = dict(clients)
    _primary = primary


def disable() -> None:
    global _clients, _primary
    _clients = {}
    _primary = None


def is_enabled() -> bool:
    return _primary is not None


def primary_name() -> Optional[str]:
    return _primary


def peer_clients() -> Dict[str, client.ApiClient]:
    return {name: c for name, c in _clients.items() if name != _primary}


def all_clients() -> Dict[str, client.ApiClient]:
    return dict(_clients)
