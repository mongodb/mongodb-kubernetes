"""Build the ``SniRoute`` list for a data cluster's mongodEnvoy from the source topology.

Kept in the ``mongod_envoy`` package (not scattered in the test) so the SNI/passthrough
routing model lives in one place. Each route maps the external pod FQDN a peer dials
(``<pod>.<externalDomain>``, published by the MC MongoDB ``externalAccess.externalDomain``)
to that pod's operator-generated external Service (``<pod>-svc-external.<ns>.svc.cluster.local``,
a ClusterIP because externalService.spec.type is overridden to ClusterIP), which the L4 Envoy
``tcp_proxy``-forwards the still-encrypted MongoDB wire stream to.

Pod naming mirrors api/mongodb/v1/mdb/mongodb_types.go + pkg/dns for an MC sharded
MongoDB:
  - shard mongod:  ``{mdb}-{shardIdx}-{clusterIdx}-{member}``
  - mongos:        ``{mdb}-mongos-{clusterIdx}-{pod}``
  - config server: ``{mdb}-config-{clusterIdx}-{member}``

``clusterIdx`` here is the SOURCE-internal cluster index (the position of the cluster in
the MongoDB ``clusterSpecList``), NOT the harness ``cluster_index``.
"""

from __future__ import annotations

from typing import List

from tests.common.mongod_envoy.mongod_envoy import SniRoute

MONGODB_WIRE_PORT = 27017


def _pod_route(mdb_name: str, pod: str, namespace: str, external_domain: str) -> SniRoute:
    # Upstream is the pod's operator-generated EXTERNAL Service. Because every component
    # (AppDB + source shard/config/mongos) overrides externalAccess.externalService to
    # spec.type=ClusterIP, the operator names the per-pod Service `<pod>-svc-external`
    # (a ClusterIP whose single endpoint is the pod) — there is NO `<pod>-svc`. Pointing
    # the SNI passthrough at `-svc-external` is what lets a member reach its own external
    # FQDN in-cluster (NLB -> Envoy -> here).
    return SniRoute(
        server_name=f"{pod}.{external_domain}",
        upstream_host=f"{pod}-svc-external.{namespace}.svc.cluster.local",
        upstream_port=MONGODB_WIRE_PORT,
    )


def build_replicaset_sni_routes(
    *,
    name: str,
    namespace: str,
    cluster_index: int,
    members: int,
    external_domain: str,
) -> List[SniRoute]:
    """One SNI route per replica-set member pod in a single member cluster.

    Used for a non-sharded MC replica set fronted by one SNI Envoy (e.g. the OM AppDB):
    pods are ``{name}-{cluster_index}-{member}``, the external FQDN is
    ``<pod>.<external_domain>`` (resolved by the external-dns wildcard to the Envoy NLB),
    and the upstream is the pod's internal per-pod headless Service ``<pod>-svc``.
    """
    return [
        _pod_route(name, f"{name}-{cluster_index}-{member_idx}", namespace, external_domain)
        for member_idx in range(members)
    ]


def build_source_sni_routes(
    *,
    mdb_name: str,
    namespace: str,
    source_cluster_index: int,
    external_domain: str,
    shard_count: int,
    shard_members: int,
    config_members: int,
    mongos_members: int,
) -> List[SniRoute]:
    """One SNI route per source pod that physically lives in this data cluster.

    ``external_domain`` is this data cluster's ``externalAccess.externalDomain``
    (e.g. ``mongodb-proxy.<clusterId>.mc.mongokubernetes.com``); the external pod FQDN is
    ``<pod>.<external_domain>``, which the external-dns wildcard ``*.<external_domain>``
    resolves to this cluster's single mongodEnvoy NLB.
    """
    routes: List[SniRoute] = []

    for pod_idx in range(mongos_members):
        routes.append(
            _pod_route(mdb_name, f"{mdb_name}-mongos-{source_cluster_index}-{pod_idx}", namespace, external_domain)
        )

    for member_idx in range(config_members):
        routes.append(
            _pod_route(mdb_name, f"{mdb_name}-config-{source_cluster_index}-{member_idx}", namespace, external_domain)
        )

    for shard_idx in range(shard_count):
        for member_idx in range(shard_members):
            routes.append(
                _pod_route(
                    mdb_name,
                    f"{mdb_name}-{shard_idx}-{source_cluster_index}-{member_idx}",
                    namespace,
                    external_domain,
                )
            )

    return routes
