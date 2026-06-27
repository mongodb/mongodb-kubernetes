"""Self-managed L4 SNI-passthrough Envoy for the data plane (the "mongodEnvoy").

This is the data-plane front for the AWS real-infra multi-cluster scenario
(``aws_simulated_mc_sharded.py``): one mongodEnvoy per data cluster, behind a single
internet-facing NLB with an external-dns wildcard, replacing per-pod LoadBalancers.
It gives the source sharded MongoDB a stable external identity reachable from the
search clusters with NO Istio mesh and NO VPC peering.

It is deliberately NOT the operator-managed / BYO ``EnvoyProxy`` in
``tests/common/search/envoy_helpers.py``. That one is an L7 (HTTP/2 + gRPC) proxy that
*terminates* mongod's TLS and re-initiates mTLS to mongot. This one is pure L4: the
``tls_inspector`` listener filter reads the TLS ClientHello SNI WITHOUT terminating,
and ``tcp_proxy`` forwards the still-encrypted MongoDB wire stream untouched to the
matching internal pod. So mongod<->mongos<->client TLS stays end-to-end; Envoy only
demultiplexes by SNI. That means no server/client certs on the proxy itself — it never
participates in the handshake.

Routing model: each external pod FQDN (published by the MC MongoDB ``externalDomain``,
e.g. ``<pod>.mongodb-proxy.<clusterId>.mc.mongokubernetes.com``) resolves via the
external-dns wildcard to the one NLB. Envoy matches that FQDN as the SNI server name and
``tcp_proxy``-forwards to the pod's internal ClusterIP/headless Service. mongos and
per-shard mongod multiplex onto a single listener port — SNI alone disambiguates them.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import List, Optional

import kubernetes
from kubernetes import client
from kubetester import create_or_update_configmap
from kubetester.kubetester import run_periodically
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


@dataclass(frozen=True)
class SniRoute:
    """One SNI-passthrough route.

    ``server_name`` is the external FQDN the client puts in the TLS SNI (the pod's
    ``externalDomain`` hostname). ``upstream_host``/``upstream_port`` is the in-cluster
    address Envoy forwards the encrypted stream to (the pod's internal Service).
    """

    server_name: str
    upstream_host: str
    upstream_port: int = 27017

    @property
    def _ident(self) -> str:
        # A stable, DNS-label-ish identifier for Envoy stat_prefix / cluster names.
        return self.server_name.replace(".", "_").replace("*", "wildcard").replace("-", "_")


class MongodEnvoy:
    """Deploys a per-cluster L4 SNI-passthrough Envoy (ConfigMap + Deployment + NLB Service).

    All Kubernetes writes go through ``api_client`` so the same helper deploys into any
    member cluster's API. AWS writes (the NLB) happen via the in-tree cloud provider
    reacting to the LoadBalancer Service — never an ad-hoc ``aws`` CLI call.
    """

    DEFAULT_IMAGE = "envoyproxy/envoy:v1.37-latest"

    def __init__(
        self,
        namespace: str,
        cluster_id: str,
        routes: List[SniRoute],
        *,
        listen_port: int = 27017,
        admin_port: int = 9901,
        name: str = "mongod-envoy",
        configmap_name: str = "mongod-envoy-config",
        service_name: str = "mongod-envoy-svc",
        external_dns_hostname: Optional[str] = None,
        lb_security_groups: Optional[List[str]] = None,
        api_client: Optional[kubernetes.client.ApiClient] = None,
        image: str = DEFAULT_IMAGE,
    ):
        if not routes:
            raise ValueError("MongodEnvoy requires at least one SniRoute")
        self.namespace = namespace
        self.cluster_id = cluster_id
        self.routes = routes
        self.listen_port = listen_port
        self.admin_port = admin_port
        self.name = name
        self.configmap_name = configmap_name
        self.service_name = service_name
        self.external_dns_hostname = external_dns_hostname
        self.lb_security_groups = lb_security_groups or []
        self.api_client = api_client
        self.image = image

    # ---- Envoy config (L4 SNI passthrough) ---------------------------------------

    def _build_filter_chain(self, route: SniRoute) -> str:
        cluster_name = f"upstream_{route._ident}"
        return f"""
        - filter_chain_match:
            server_names:
            - "{route.server_name}"
          filters:
          - name: envoy.filters.network.tcp_proxy
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy
              stat_prefix: sni_{route._ident}
              cluster: {cluster_name}"""

    def _build_cluster(self, route: SniRoute) -> str:
        cluster_name = f"upstream_{route._ident}"
        # No transport_socket: passthrough. Envoy forwards the raw TLS bytes; the
        # MongoDB handshake completes between the real client and the real mongod/mongos.
        return f"""
      - name: {cluster_name}
        type: STRICT_DNS
        lb_policy: ROUND_ROBIN
        load_assignment:
          cluster_name: {cluster_name}
          endpoints:
          - lb_endpoints:
            - endpoint:
                address:
                  socket_address:
                    address: {route.upstream_host}
                    port_value: {route.upstream_port}
        upstream_connection_options:
          tcp_keepalive:
            keepalive_time: 10
            keepalive_interval: 3
            keepalive_probes: 3"""

    def _build_config(self) -> str:
        filter_chains = "".join(self._build_filter_chain(r) for r in self.routes)
        clusters = "".join(self._build_cluster(r) for r in self.routes)
        return f"""admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: {self.admin_port}

static_resources:
  listeners:
  - name: mongod_sni_listener
    address:
      socket_address:
        address: 0.0.0.0
        port_value: {self.listen_port}
    listener_filters:
    - name: envoy.filters.listener.tls_inspector
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
    filter_chains:{filter_chains}

  clusters:{clusters}

layered_runtime:
  layers:
  - name: static_layer
    static_layer:
      overload:
        global_downstream_max_connections: 50000
"""

    def create_configmap(self) -> None:
        create_or_update_configmap(
            self.namespace,
            self.configmap_name,
            {"envoy.yaml": self._build_config()},
            api_client=self.api_client,
        )
        logger.info(
            f"[{self.cluster_id}] mongodEnvoy ConfigMap {self.configmap_name} created "
            f"with {len(self.routes)} SNI routes"
        )

    # ---- Workload (Deployment + NLB Service) -------------------------------------

    def _deployment_manifest(self) -> dict:
        labels = {"app": self.name, "component": "mongod-envoy"}
        return {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {"name": self.name, "labels": labels},
            "spec": {
                "replicas": 1,
                "selector": {"matchLabels": {"app": self.name}},
                "template": {
                    "metadata": {"labels": labels},
                    "spec": {
                        "containers": [
                            {
                                "name": "envoy",
                                "image": self.image,
                                "command": ["/usr/local/bin/envoy"],
                                "args": ["-c", "/etc/envoy/envoy.yaml", "--log-level", "info"],
                                "ports": [
                                    {"name": "mongod-sni", "containerPort": self.listen_port},
                                    {"name": "admin", "containerPort": self.admin_port},
                                ],
                                "resources": {
                                    "requests": {"cpu": "100m", "memory": "128Mi"},
                                    "limits": {"cpu": "500m", "memory": "512Mi"},
                                },
                                "readinessProbe": {
                                    "httpGet": {"path": "/ready", "port": self.admin_port},
                                    "initialDelaySeconds": 5,
                                    "periodSeconds": 5,
                                },
                                "volumeMounts": [
                                    {"name": "envoy-config", "mountPath": "/etc/envoy", "readOnly": True},
                                ],
                                "securityContext": {
                                    "allowPrivilegeEscalation": False,
                                    "capabilities": {"drop": ["ALL"]},
                                },
                            }
                        ],
                        "securityContext": {
                            "runAsNonRoot": True,
                            "runAsUser": 2000,
                            "seccompProfile": {"type": "RuntimeDefault"},
                        },
                        "volumes": [
                            {"name": "envoy-config", "configMap": {"name": self.configmap_name}},
                        ],
                    },
                },
            },
        }

    def create_deployment(self) -> None:
        apps = client.AppsV1Api(api_client=self.api_client)
        manifest = self._deployment_manifest()
        try:
            apps.create_namespaced_deployment(self.namespace, manifest)
            logger.info(f"[{self.cluster_id}] mongodEnvoy Deployment {self.name} created")
        except kubernetes.client.ApiException as e:
            if e.status == 409:
                apps.patch_namespaced_deployment(self.name, self.namespace, manifest)
                logger.info(f"[{self.cluster_id}] mongodEnvoy Deployment {self.name} updated")
            else:
                raise

    def _service_annotations(self) -> dict:
        # internet-facing NLB; corp-prefix-locked via the security-group annotation.
        # Never 0.0.0.0/0 — the SG must come from the corp managed prefix list.
        annotations = {
            "service.beta.kubernetes.io/aws-load-balancer-type": "external",
            "service.beta.kubernetes.io/aws-load-balancer-nlb-target-type": "instance",
            "service.beta.kubernetes.io/aws-load-balancer-scheme": "internet-facing",
        }
        if self.lb_security_groups:
            annotations["service.beta.kubernetes.io/aws-load-balancer-security-groups"] = ",".join(
                self.lb_security_groups
            )
            # BYO frontend SG ⇒ the AWS LB Controller no longer auto-opens the node SG to
            # the NLB; without this the targets fail health checks (i/o timeout).
            annotations[
                "service.beta.kubernetes.io/aws-load-balancer-manage-backend-security-group-rules"
            ] = "true"
        if self.external_dns_hostname:
            annotations["external-dns.alpha.kubernetes.io/hostname"] = self.external_dns_hostname
        return annotations

    def _service_manifest(self) -> dict:
        return {
            "apiVersion": "v1",
            "kind": "Service",
            "metadata": {
                "name": self.service_name,
                "labels": {"app": self.name},
                "annotations": self._service_annotations(),
            },
            "spec": {
                "type": "LoadBalancer",
                "selector": {"app": self.name},
                "ports": [
                    {"name": "mongod-sni", "port": self.listen_port, "targetPort": self.listen_port},
                ],
            },
        }

    def create_service(self) -> None:
        if self.lb_security_groups == []:
            # Fail closed: an internet-facing NLB with no corp SG would default to open.
            raise ValueError(
                f"[{self.cluster_id}] mongodEnvoy refuses to create an internet-facing NLB without "
                "a corp security group; pass lb_security_groups from the provisioned prefix-locked SGs"
            )
        core = client.CoreV1Api(api_client=self.api_client)
        manifest = self._service_manifest()
        try:
            core.create_namespaced_service(self.namespace, manifest)
            logger.info(f"[{self.cluster_id}] mongodEnvoy Service {self.service_name} (LoadBalancer) created")
        except kubernetes.client.ApiException as e:
            if e.status == 409:
                core.patch_namespaced_service(self.service_name, self.namespace, manifest)
                logger.info(f"[{self.cluster_id}] mongodEnvoy Service {self.service_name} updated")
            else:
                raise

    # ---- Lifecycle ----------------------------------------------------------------

    def wait_for_ready(self, timeout: int = 180) -> None:
        apps = client.AppsV1Api(api_client=self.api_client)

        def check() -> tuple:
            try:
                dep = apps.read_namespaced_deployment(self.name, self.namespace)
                ready = dep.status.ready_replicas or 0
                return ready >= 1, f"ready_replicas={ready}"
            except Exception as e:  # noqa: BLE001 - surface any read error as not-ready
                return False, f"Deployment {self.name} not readable: {e}"

        run_periodically(
            check, timeout=timeout, sleep_time=5, msg=f"[{self.cluster_id}] mongodEnvoy {self.name} ready"
        )

    def lb_hostname(self, timeout: int = 300) -> str:
        """Block until the NLB is provisioned and return its external hostname.

        external-dns publishes ``external_dns_hostname`` as a CNAME to this; callers
        usually use the stable external-dns name, but this is handy for diagnostics.
        """
        core = client.CoreV1Api(api_client=self.api_client)

        result: dict = {}

        def check() -> tuple:
            svc = core.read_namespaced_service(self.service_name, self.namespace)
            ingress = (svc.status.load_balancer.ingress or []) if svc.status.load_balancer else []
            if ingress and (ingress[0].hostname or ingress[0].ip):
                result["host"] = ingress[0].hostname or ingress[0].ip
                return True, f"NLB provisioned: {result['host']}"
            return False, "NLB hostname not yet assigned"

        run_periodically(
            check, timeout=timeout, sleep_time=10, msg=f"[{self.cluster_id}] mongodEnvoy NLB provisioning"
        )
        return result["host"]

    def deploy(self) -> None:
        self.create_configmap()
        self.create_deployment()
        self.create_service()
        self.wait_for_ready()
        logger.info(f"[{self.cluster_id}] mongodEnvoy deployed ({len(self.routes)} SNI routes)")
