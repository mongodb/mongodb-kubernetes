"""Cloud-QA OM project listing + cleanup.

For ``om list`` we make a direct REST call (read-only) so we don't have to
go through the in-container delete script. Cleanup (``om clean``) wraps
``scripts/dev/delete_om_projects.sh``.
"""

from __future__ import annotations

import json
import os
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Optional

from ..errors import ExternalCommandFailed, ToolMissing, WtCtlError
from ..runner import Runner
from ..state import OmState


def _read_context_env(worktree_root: Path) -> dict[str, str]:
    """Read the rendered ``.generated/context.env`` (a `.env`-style file).
    Returns an empty dict when the file is absent.
    """
    out: dict[str, str] = {}
    f = worktree_root / ".generated" / "context.env"
    if not f.is_file():
        return out
    for raw in f.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            continue
        k, v = line.split("=", 1)
        v = v.strip()
        if (v.startswith('"') and v.endswith('"')) or (v.startswith("'") and v.endswith("'")):
            v = v[1:-1]
        out[k.strip()] = v
    return out


class OmDomain:
    def __init__(self, runner: Runner, repo_root: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root

    # ------------------------------------------------------------------
    def state_for(self, worktree_root: Path) -> OmState:
        ctx = _read_context_env(worktree_root)
        ns = ctx.get("NAMESPACE")
        scope = f"{ns}, {ns}-*" if ns else None
        count = self._count_projects_in_scope(ctx) if ns else None
        return OmState(namespace=ns, project_count=count, scope=scope)

    def list_projects(self, worktree_root: Path) -> list[dict]:
        ctx = _read_context_env(worktree_root)
        ns = ctx.get("NAMESPACE")
        if not ns:
            return []
        return self._fetch_projects(ctx, ns)

    # ------------------------------------------------------------------
    def clean(self, worktree_root: Path) -> None:
        env = _read_context_env(worktree_root)
        env.setdefault("PROJECT_DIR", str(worktree_root))
        argv = [str(self.repo_root / "scripts/dev/delete_om_projects.sh")]
        self.runner.run_streaming(argv, prefix="[om-clean] ", env=env, cwd=worktree_root)

    # ------------------------------------------------------------------
    def _fetch_projects(self, ctx: dict[str, str], ns: str) -> list[dict]:
        """Single GET against /api/public/v1.0/groups, filter client-side by
        the same prefix that delete_om_projects.sh uses (``${ns}`` and
        ``${ns}-*``).

        Raises ``WtCtlError`` on any non-transient failure (HTTP error,
        auth, bad JSON). Network OSErrors (DNS, timeout) return ``[]``
        with a stderr warning — those are routinely flaky and we don't
        want ``wt-ctl status`` to die on a slow VPN.
        """
        import sys

        host = ctx.get("OM_HOST") or os.environ.get("OM_HOST")
        user = ctx.get("OM_USER") or os.environ.get("OM_USER")
        api_key = ctx.get("OM_API_KEY") or os.environ.get("OM_API_KEY")
        if not (host and user and api_key):
            return []
        url = f"{host.rstrip('/')}/api/public/v1.0/groups?itemsPerPage=200"
        try:
            data = _digest_get_json(url, user, api_key)
        except urllib.error.HTTPError as exc:
            raise WtCtlError(
                f"OM /groups query failed: HTTP {exc.code} {exc.reason} "
                f"(host={host}, user={user}); check OM_USER/OM_API_KEY in "
                f"context.env"
            ) from exc
        except urllib.error.URLError as exc:
            sys.stderr.write(f"[wt-ctl om] WARN: network error querying {host} ({exc}); " f"reporting 0 projects.\n")
            return []
        except ValueError as exc:
            # Non-JSON body (a proxy / load-balancer HTML error page, a
            # truncated response, ...). json.loads raises JSONDecodeError, a
            # ValueError; degrade like a network error rather than killing the
            # read-only `wt-ctl status` render.
            sys.stderr.write(f"[wt-ctl om] WARN: non-JSON response from {host} ({exc}); reporting 0 projects.\n")
            return []
        if not isinstance(data, dict):
            return []
        results = data.get("results") or []
        out: list[dict] = []
        for g in results:
            name = g.get("name") or ""
            if name == ns or name.startswith(f"{ns}-"):
                out.append(g)
        return out

    def _count_projects_in_scope(self, ctx: dict[str, str]) -> Optional[int]:
        ns = ctx.get("NAMESPACE")
        if not ns:
            return None
        try:
            return len(self._fetch_projects(ctx, ns))
        except WtCtlError as exc:
            # `om` is a status-time call; render-error path: report None so
            # the row reads "om <err>: ..." instead of dying the whole
            # `wt-ctl status` render.
            import sys

            sys.stderr.write(f"[wt-ctl om] WARN: {exc}\n")
            return None


# ---------------------------------------------------------------------------
# tiny digest GET helper (stdlib only)
# ---------------------------------------------------------------------------


def _digest_get_json(url: str, user: str, api_key: str):
    """Minimal HTTP-Digest GET. We avoid pip deps; OM uses MD5 digest auth.

    Realm is left None so the digest handler matches whatever the server
    advertises in its WWW-Authenticate header (OM's is "MMS Public API"). A
    pinned realm that differs makes urllib silently skip the credentialed retry
    and the request 401s.
    """
    mgr = urllib.request.HTTPPasswordMgrWithDefaultRealm()
    mgr.add_password(None, url, user, api_key)
    handler = urllib.request.HTTPDigestAuthHandler(mgr)
    opener = urllib.request.build_opener(handler)
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    # Very short timeout — `om list` is a status-time call and we don't want
    # to block status rendering when cloud-qa is slow.
    with opener.open(req, timeout=5) as resp:
        return json.loads(resp.read().decode("utf-8"))
