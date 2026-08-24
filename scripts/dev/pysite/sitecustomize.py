"""Route the Python Kubernetes client through the devcontainer's HTTP proxy.

kubectl honours ``clusters[*].cluster.proxy-url``; the Python client does not
(``kube_config.py`` never populates ``Configuration.proxy``), so pytest inside
the devcontainer dials the EVG host's ``127.0.0.1:<apiserver-port>`` directly
and gets ECONNREFUSED. Applied only when ``MCK_K8S_PY_PROXY`` is set, so
local-kind stacks (no proxy chain) are unaffected.

Imported automatically by ``site`` when this directory is on PYTHONPATH.
"""

import os

_proxy = os.environ.get("MCK_K8S_PY_PROXY", "").strip()

if _proxy:
    try:
        from kubernetes.client.configuration import Configuration

        _orig_init = Configuration.__init__

        def _init(self, *args, **kwargs):
            _orig_init(self, *args, **kwargs)
            if not self.proxy:
                self.proxy = _proxy
                self.no_proxy = ""

        Configuration.__init__ = _init
    except Exception:  # kubernetes not installed — nothing to patch
        pass
