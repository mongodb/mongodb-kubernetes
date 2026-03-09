from kubernetes import client
from kubetester import create_or_update_configmap
from kubetester.certs import create_tls_certs
from kubetester.kubetester import KubernetesTester, run_periodically
from tests import test_logger
from tests.common.search import search_resource_names

logger = test_logger.get_test_logger(__name__)


class EnvoyProxy:

    SERVER_CERT_SECRET = "envoy-server-cert-pem"
    CLIENT_CERT_SECRET = "envoy-client-cert-pem"

    def __init__(
        self,
        namespace: str,
        ca_configmap_name: str,
        mdbs_resource_name: str,
        shard_count: int = 0,
        mdb_resource_name: str = "",
        proxy_svc_name: str = "envoy-proxy-svc",
        name: str = "envoy-proxy",
        configmap_name: str = "envoy-config",
        mongot_port: int = 27028,
        envoy_proxy_port: int = 27029,
        envoy_admin_port: int = 9901,
    ):
        self.namespace = namespace
        self.ca_configmap_name = ca_configmap_name
        self.mdb_resource_name = mdb_resource_name
        self.mdbs_resource_name = mdbs_resource_name
        self.shard_count = shard_count
        self.proxy_svc_name = proxy_svc_name
        self.name = name
        self.configmap_name = configmap_name
        self.mongot_port = mongot_port
        self.envoy_proxy_port = envoy_proxy_port
        self.envoy_admin_port = envoy_admin_port

    def _shard_name(self, idx: int) -> str:
        return f"{self.mdb_resource_name}-{idx}"

    def create_certificates(self, issuer: str):
        logger.info("Creating Envoy proxy certificates...")

        additional_domains = []
        if self.shard_count > 0:
            for i in range(self.shard_count):
                shard_name = self._shard_name(i)
                proxy_svc = search_resource_names.shard_proxy_service_name(self.mdbs_resource_name, shard_name)
                additional_domains.append(f"{proxy_svc}.{self.namespace}.svc.cluster.local")
        else:
            additional_domains.append(f"{self.proxy_svc_name}.{self.namespace}.svc.cluster.local")
        additional_domains.append(f"*.{self.namespace}.svc.cluster.local")

        create_tls_certs(
            issuer=issuer,
            namespace=self.namespace,
            resource_name="envoy-server",
            replicas=1,
            service_name=self.name,
            additional_domains=additional_domains,
            secret_name=self.SERVER_CERT_SECRET,
        )
        logger.info("Envoy server certificate created")

        create_tls_certs(
            issuer=issuer,
            namespace=self.namespace,
            resource_name="envoy-client",
            replicas=1,
            service_name="envoy-proxy-client",
            additional_domains=[f"*.{self.namespace}.svc.cluster.local"],
            secret_name=self.CLIENT_CERT_SECRET,
        )
        logger.info("Envoy client certificate created")

    def _build_filter_chain(self, sni_host: str, stat_prefix: str, route_name: str, backend_name: str, cluster_name: str):
        return f"""
        - filter_chain_match:
            server_names:
            - "{sni_host}"
          filters:
          - name: envoy.filters.network.http_connection_manager
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
              stat_prefix: {stat_prefix}
              codec_type: AUTO
              route_config:
                name: {route_name}
                virtual_hosts:
                - name: {backend_name}
                  domains: ["*"]
                  routes:
                  - match:
                      prefix: "/"
                      grpc: {{}}
                    route:
                      cluster: {cluster_name}
                      timeout: 300s
              http_filters:
              - name: envoy.filters.http.router
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
              http2_protocol_options:
                initial_connection_window_size: 1048576
                initial_stream_window_size: 1048576
              stream_idle_timeout: 300s
              request_timeout: 300s
          transport_socket:
            name: envoy.transport_sockets.tls
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
              common_tls_context:
                tls_certificates:
                - certificate_chain:
                    filename: /etc/envoy/tls/server/tls.crt
                  private_key:
                    filename: /etc/envoy/tls/server/tls.key
                validation_context:
                  trusted_ca:
                    filename: /etc/envoy/tls/ca/ca-pem
                tls_params:
                  tls_minimum_protocol_version: TLSv1_2
                  tls_maximum_protocol_version: TLSv1_2
                alpn_protocols:
                - "h2"
              require_client_certificate: true"""

    def _build_cluster(self, cluster_name: str, backend_address: str):
        return f"""
      - name: {cluster_name}
        type: STRICT_DNS
        lb_policy: ROUND_ROBIN
        http2_protocol_options:
          initial_connection_window_size: 1048576
          initial_stream_window_size: 1048576
        load_assignment:
          cluster_name: {cluster_name}
          endpoints:
          - lb_endpoints:
            - endpoint:
                address:
                  socket_address:
                    address: {backend_address}
                    port_value: {self.mongot_port}
        circuit_breakers:
          thresholds:
          - priority: DEFAULT
            max_connections: 1024
            max_pending_requests: 1024
            max_requests: 1024
            max_retries: 3
        transport_socket:
          name: envoy.transport_sockets.tls
          typed_config:
            "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
            common_tls_context:
              tls_certificates:
              - certificate_chain:
                  filename: /etc/envoy/tls/client/tls.crt
                private_key:
                  filename: /etc/envoy/tls/client/tls.key
              validation_context:
                trusted_ca:
                  filename: /etc/envoy/tls/ca/ca-pem
              alpn_protocols:
              - "h2"
            sni: {backend_address}
        upstream_connection_options:
          tcp_keepalive:
            keepalive_time: 10
            keepalive_interval: 3
            keepalive_probes: 3
        common_http_protocol_options:
          idle_timeout: 300s"""

    def _build_filter_chains_and_clusters(self):
        filter_chains = ""
        clusters = ""

        if self.shard_count > 0:
            for i in range(self.shard_count):
                shard_name = self._shard_name(i)
                proxy_svc = search_resource_names.shard_proxy_service_name(self.mdbs_resource_name, shard_name)
                search_svc = search_resource_names.shard_service_name(self.mdbs_resource_name, shard_name)
                cluster_name = f"mongot_{shard_name.replace('-', '_')}_cluster"
                backend_address = f"{search_svc}.{self.namespace}.svc.cluster.local"

                filter_chains += self._build_filter_chain(
                    sni_host=f"{proxy_svc}.{self.namespace}.svc.cluster.local",
                    stat_prefix=f"ingress_{shard_name.replace('-', '_')}",
                    route_name=f"{shard_name}_route",
                    backend_name=f"mongot_{shard_name.replace('-', '_')}_backend",
                    cluster_name=cluster_name,
                )
                clusters += self._build_cluster(cluster_name, backend_address)
        else:
            search_svc = search_resource_names.mongot_service_name(self.mdbs_resource_name)
            cluster_name = "mongot_cluster"
            backend_address = f"{search_svc}.{self.namespace}.svc.cluster.local"

            filter_chains += self._build_filter_chain(
                sni_host=f"{self.proxy_svc_name}.{self.namespace}.svc.cluster.local",
                stat_prefix="ingress_mongot",
                route_name="mongot_route",
                backend_name="mongot_backend",
                cluster_name=cluster_name,
            )
            clusters += self._build_cluster(cluster_name, backend_address)

        return filter_chains, clusters

    def create_configmap(self):
        filter_chains, clusters = self._build_filter_chains_and_clusters()

        envoy_config = f"""admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: {self.envoy_admin_port}

static_resources:
  listeners:
  - name: mongod_listener
    address:
      socket_address:
        address: 0.0.0.0
        port_value: {self.envoy_proxy_port}
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

        create_or_update_configmap(self.namespace, self.configmap_name, {"envoy.yaml": envoy_config})
        if self.shard_count > 0:
            logger.info(f"Envoy ConfigMap created with routing for {self.shard_count} shards")
        else:
            logger.info("Envoy ConfigMap created with routing to mongot service")

    def create_deployment(self):
        deployment = {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {
                "name": self.name,
                "labels": {"app": self.name, "component": "search-proxy"},
            },
            "spec": {
                "replicas": 1,
                "selector": {"matchLabels": {"app": self.name}},
                "template": {
                    "metadata": {"labels": {"app": self.name, "component": "search-proxy"}},
                    "spec": {
                        "containers": [
                            {
                                "name": "envoy",
                                "image": "envoyproxy/envoy:v1.31-latest",
                                "command": ["/usr/local/bin/envoy"],
                                "args": ["-c", "/etc/envoy/envoy.yaml", "--log-level", "info"],
                                "ports": [
                                    {"name": "grpc", "containerPort": self.envoy_proxy_port},
                                    {"name": "admin", "containerPort": self.envoy_admin_port},
                                ],
                                "resources": {
                                    "requests": {"cpu": "100m", "memory": "128Mi"},
                                    "limits": {"cpu": "500m", "memory": "512Mi"},
                                },
                                "readinessProbe": {
                                    "httpGet": {"path": "/ready", "port": self.envoy_admin_port},
                                    "initialDelaySeconds": 5,
                                    "periodSeconds": 5,
                                },
                                "volumeMounts": [
                                    {"name": "envoy-config", "mountPath": "/etc/envoy", "readOnly": True},
                                    {
                                        "name": "envoy-server-cert",
                                        "mountPath": "/etc/envoy/tls/server",
                                        "readOnly": True,
                                    },
                                    {
                                        "name": "envoy-client-cert",
                                        "mountPath": "/etc/envoy/tls/client",
                                        "readOnly": True,
                                    },
                                    {"name": "ca-cert", "mountPath": "/etc/envoy/tls/ca", "readOnly": True},
                                ],
                            }
                        ],
                        "volumes": [
                            {"name": "envoy-config", "configMap": {"name": self.configmap_name}},
                            {"name": "envoy-server-cert", "secret": {"secretName": self.SERVER_CERT_SECRET}},
                            {"name": "envoy-client-cert", "secret": {"secretName": self.CLIENT_CERT_SECRET}},
                            {
                                "name": "ca-cert",
                                "configMap": {
                                    "name": self.ca_configmap_name,
                                    "items": [{"key": "ca-pem", "path": "ca-pem"}],
                                },
                            },
                        ],
                    },
                },
            },
        }

        try:
            KubernetesTester.create_deployment(self.namespace, deployment)
            logger.info("Envoy Deployment created")
        except Exception as e:
            logger.info(f"Envoy Deployment may already exist: {e}")

    def _create_service(self, svc_name: str, extra_labels: dict = None):
        svc_labels = {"app": self.name}
        if extra_labels:
            svc_labels.update(extra_labels)
        service = {
            "apiVersion": "v1",
            "kind": "Service",
            "metadata": {"name": svc_name, "labels": svc_labels},
            "spec": {
                "type": "ClusterIP",
                "selector": {"app": self.name},
                "ports": [{"name": "grpc", "port": self.envoy_proxy_port, "targetPort": self.envoy_proxy_port}],
            },
        }
        try:
            KubernetesTester.create_service(self.namespace, service)
            logger.info(f"Proxy Service {svc_name} created")
        except Exception as e:
            logger.info(f"Proxy Service {svc_name} may already exist: {e}")

    def create_services(self):
        if self.shard_count > 0:
            for i in range(self.shard_count):
                shard_name = self._shard_name(i)
                proxy_svc_name = search_resource_names.shard_proxy_service_name(self.mdbs_resource_name, shard_name)
                self._create_service(proxy_svc_name, extra_labels={"target-shard": shard_name})
        else:
            self._create_service(self.proxy_svc_name)

    def wait_for_ready(self, timeout: int = 120):
        def check_envoy_ready():
            try:
                apps_v1 = client.AppsV1Api()
                deployment = apps_v1.read_namespaced_deployment(self.name, self.namespace)
                ready = deployment.status.ready_replicas or 0
                return ready >= 1, f"Envoy ready replicas: {ready}"
            except Exception as e:
                return False, f"Error checking Envoy: {e}"

        run_periodically(check_envoy_ready, timeout=timeout, sleep_time=5, msg="Envoy proxy to be ready")

    def deploy(self):
        self.create_configmap()
        self.create_deployment()
        self.create_services()
        self.wait_for_ready()
        logger.info("Envoy proxy deployed successfully")
