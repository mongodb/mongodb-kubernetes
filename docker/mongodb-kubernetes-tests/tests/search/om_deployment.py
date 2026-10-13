"""Backwards-compatible re-export: the shared implementation now lives in tests.common.ops_manager."""

from tests.common.ops_manager.om_deployment import get_ops_manager

__all__ = ["get_ops_manager"]
