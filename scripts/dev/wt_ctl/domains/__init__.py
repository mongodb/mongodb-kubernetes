"""Domain modules. None of these may import ``subprocess`` — they must
receive a ``Runner`` instance via constructor injection.

Enforced by ``scripts/test/wt_ctl_lint.py``.
"""
