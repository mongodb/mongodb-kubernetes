package operator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
)

func testRoute(shardName string) envoyRoute {
	return envoyRoute{
		Name:          shardName,
		NameSafe:      "mdb_sh_0",
		SNIHostname:   shardName + "-proxy.ns.svc.cluster.local",
		UpstreamHosts: []string{shardName + "-mongot.ns.svc.cluster.local"},
		UpstreamPort:  27028,
	}
}

func testCAKeyName() string {
	return "ca-pem"
}

func unmarshalBootstrap(t *testing.T, jsonStr string) *bootstrapv3.Bootstrap {
	t.Helper()
	bootstrap := &bootstrapv3.Bootstrap{}
	err := protojson.Unmarshal([]byte(jsonStr), bootstrap)
	require.NoError(t, err, "failed to unmarshal bootstrap config")
	return bootstrap
}

func unmarshalDiscoveryResponse(t *testing.T, jsonStr string) *discoveryv3.DiscoveryResponse {
	t.Helper()
	resp := &discoveryv3.DiscoveryResponse{}
	err := protojson.Unmarshal([]byte(jsonStr), resp)
	require.NoError(t, err, "failed to unmarshal DiscoveryResponse")
	return resp
}

func TestBuildBootstrapJSON(t *testing.T) {
	result, err := buildBootstrapJSON()
	require.NoError(t, err)
	assert.True(t, json.Valid([]byte(result)))

	bootstrap := unmarshalBootstrap(t, result)

	// Node (required for xDS subscriptions)
	require.NotNil(t, bootstrap.Node)
	assert.Equal(t, "envoy-search-proxy", bootstrap.Node.Id)
	assert.Equal(t, "search-proxy", bootstrap.Node.Cluster)

	// Admin
	require.NotNil(t, bootstrap.Admin)
	adminAddr := bootstrap.Admin.Address.GetSocketAddress()
	assert.Equal(t, "0.0.0.0", adminAddr.GetAddress())
	assert.Equal(t, uint32(EnvoyAdminPort), adminAddr.GetPortValue())

	// No static resources
	assert.Nil(t, bootstrap.StaticResources)

	// Dynamic resources with filesystem xDS
	require.NotNil(t, bootstrap.DynamicResources)
	require.NotNil(t, bootstrap.DynamicResources.CdsConfig)
	require.NotNil(t, bootstrap.DynamicResources.LdsConfig)

	cdsSrc := bootstrap.DynamicResources.CdsConfig.GetPathConfigSource()
	require.NotNil(t, cdsSrc)
	assert.Equal(t, "/etc/envoy/cds.json", cdsSrc.Path)
	require.NotNil(t, cdsSrc.WatchedDirectory)
	assert.Equal(t, "/etc/envoy", cdsSrc.WatchedDirectory.Path)

	ldsSrc := bootstrap.DynamicResources.LdsConfig.GetPathConfigSource()
	require.NotNil(t, ldsSrc)
	assert.Equal(t, "/etc/envoy/lds.json", ldsSrc.Path)
	require.NotNil(t, ldsSrc.WatchedDirectory)
	assert.Equal(t, "/etc/envoy", ldsSrc.WatchedDirectory.Path)

	// Layered runtime
	require.NotNil(t, bootstrap.LayeredRuntime)
	require.Len(t, bootstrap.LayeredRuntime.Layers, 1)
	assert.Equal(t, "static_layer", bootstrap.LayeredRuntime.Layers[0].Name)
}

func TestBuildCDSJSON_SingleShard_NoTLS(t *testing.T) {
	route := testRoute("mdb-sh-0")
	result, err := buildCDSJSON([]envoyRoute{route}, false, testCAKeyName())
	require.NoError(t, err)
	assert.True(t, json.Valid([]byte(result)))

	resp := unmarshalDiscoveryResponse(t, result)
	assert.Equal(t, "type.googleapis.com/envoy.config.cluster.v3.Cluster", resp.TypeUrl)
	require.Len(t, resp.Resources, 1)

	cluster := &clusterv3.Cluster{}
	err = resp.Resources[0].UnmarshalTo(cluster)
	require.NoError(t, err)
	assert.Equal(t, "mongot_mdb_sh_0_cluster", cluster.Name)
	assert.Equal(t, clusterv3.Cluster_STRICT_DNS, cluster.GetType())
	assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
	assert.Nil(t, cluster.TransportSocket, "no TLS when disabled")

	// Endpoint
	require.Len(t, cluster.LoadAssignment.Endpoints, 1)
	require.Len(t, cluster.LoadAssignment.Endpoints[0].LbEndpoints, 1)
	ep := cluster.LoadAssignment.Endpoints[0].LbEndpoints[0].GetEndpoint()
	assert.Equal(t, route.UpstreamHosts[0], ep.Address.GetSocketAddress().GetAddress())
	assert.Equal(t, uint32(route.UpstreamPort), ep.Address.GetSocketAddress().GetPortValue())

	// Circuit breakers
	require.NotNil(t, cluster.CircuitBreakers)
	require.Len(t, cluster.CircuitBreakers.Thresholds, 1)
	assert.Equal(t, corev3.RoutingPriority_DEFAULT, cluster.CircuitBreakers.Thresholds[0].Priority)
	assert.Equal(t, uint32(1024), cluster.CircuitBreakers.Thresholds[0].MaxConnections.GetValue())

	// TCP keepalive
	require.NotNil(t, cluster.UpstreamConnectionOptions)
	require.NotNil(t, cluster.UpstreamConnectionOptions.TcpKeepalive)
	assert.Equal(t, uint32(10), cluster.UpstreamConnectionOptions.TcpKeepalive.KeepaliveTime.GetValue())
}

func TestBuildCDSJSON_SingleShard_WithTLS(t *testing.T) {
	route := testRoute("mdb-sh-0")
	result, err := buildCDSJSON([]envoyRoute{route}, true, testCAKeyName())
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	require.Len(t, resp.Resources, 1)

	cluster := &clusterv3.Cluster{}
	err = resp.Resources[0].UnmarshalTo(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.TransportSocket, "upstream TLS should be present")

	upstreamTLS := &tlsv3.UpstreamTlsContext{}
	err = cluster.TransportSocket.GetTypedConfig().UnmarshalTo(upstreamTLS)
	require.NoError(t, err)
	assert.Equal(t, route.UpstreamHosts[0], upstreamTLS.Sni)
	assert.Equal(t, []string{"h2"}, upstreamTLS.CommonTlsContext.AlpnProtocols)
}

func TestBuildCDSJSON_MultipleShards(t *testing.T) {
	routes := []envoyRoute{
		{Name: "mdb-sh-0", NameSafe: "mdb_sh_0", SNIHostname: "s0.ns.svc.cluster.local", UpstreamHosts: []string{"m0.ns.svc.cluster.local"}, UpstreamPort: 27028},
		{Name: "mdb-sh-1", NameSafe: "mdb_sh_1", SNIHostname: "s1.ns.svc.cluster.local", UpstreamHosts: []string{"m1.ns.svc.cluster.local"}, UpstreamPort: 27028},
	}
	result, err := buildCDSJSON(routes, false, testCAKeyName())
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	require.Len(t, resp.Resources, 2)

	for i, route := range routes {
		cluster := &clusterv3.Cluster{}
		err = resp.Resources[i].UnmarshalTo(cluster)
		require.NoError(t, err)
		assert.Equal(t, "mongot_"+route.NameSafe+"_cluster", cluster.Name)
	}
}

func TestBuildLDSJSON_SingleShard_NoTLS(t *testing.T) {
	route := testRoute("mdb-sh-0")
	result, err := buildLDSJSON([]envoyRoute{route}, false, testCAKeyName(), nil)
	require.NoError(t, err)
	assert.True(t, json.Valid([]byte(result)))

	resp := unmarshalDiscoveryResponse(t, result)
	assert.Equal(t, "type.googleapis.com/envoy.config.listener.v3.Listener", resp.TypeUrl)
	require.Len(t, resp.Resources, 1)

	listener := &listenerv3.Listener{}
	err = resp.Resources[0].UnmarshalTo(listener)
	require.NoError(t, err)
	assert.Equal(t, "mongod_listener", listener.Name)
	assert.Equal(t, uint32(searchv1.EnvoyDefaultProxyPort), listener.Address.GetSocketAddress().GetPortValue())
	assert.Empty(t, listener.ListenerFilters, "no TLS Inspector when TLS disabled")
	require.Len(t, listener.FilterChains, 1)
	assert.Nil(t, listener.FilterChains[0].FilterChainMatch, "no SNI match when TLS disabled")
}

func TestBuildLDSJSON_SingleShard_WithTLS(t *testing.T) {
	route := testRoute("mdb-sh-0")
	result, err := buildLDSJSON([]envoyRoute{route}, true, testCAKeyName(), nil)
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	require.Len(t, resp.Resources, 1)

	listener := &listenerv3.Listener{}
	err = resp.Resources[0].UnmarshalTo(listener)
	require.NoError(t, err)

	// TLS Inspector present
	require.Len(t, listener.ListenerFilters, 1)
	assert.Contains(t, listener.ListenerFilters[0].Name, "tls_inspector")

	// Filter chain has downstream TLS and SNI match
	require.Len(t, listener.FilterChains, 1)
	fc := listener.FilterChains[0]
	require.NotNil(t, fc.FilterChainMatch)
	assert.Equal(t, []string{route.SNIHostname}, fc.FilterChainMatch.ServerNames)
	require.NotNil(t, fc.TransportSocket)

	downstreamTLS := &tlsv3.DownstreamTlsContext{}
	err = fc.TransportSocket.GetTypedConfig().UnmarshalTo(downstreamTLS)
	require.NoError(t, err)
	assert.True(t, downstreamTLS.RequireClientCertificate.GetValue())
	assert.Equal(t, tlsv3.TlsParameters_TLSv1_3, downstreamTLS.CommonTlsContext.TlsParams.TlsMinimumProtocolVersion)
}

func TestBuildLDSJSON_MultipleShards_WithTLS(t *testing.T) {
	routes := []envoyRoute{
		{Name: "mdb-sh-0", NameSafe: "mdb_sh_0", SNIHostname: "shard0.ns.svc.cluster.local", UpstreamHosts: []string{"mongot0.ns.svc.cluster.local"}, UpstreamPort: 27028},
		{Name: "mdb-sh-1", NameSafe: "mdb_sh_1", SNIHostname: "shard1.ns.svc.cluster.local", UpstreamHosts: []string{"mongot1.ns.svc.cluster.local"}, UpstreamPort: 27028},
		{Name: "mdb-sh-2", NameSafe: "mdb_sh_2", SNIHostname: "shard2.ns.svc.cluster.local", UpstreamHosts: []string{"mongot2.ns.svc.cluster.local"}, UpstreamPort: 27028},
	}

	result, err := buildLDSJSON(routes, true, testCAKeyName(), nil)
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	listener := &listenerv3.Listener{}
	err = resp.Resources[0].UnmarshalTo(listener)
	require.NoError(t, err)

	require.Len(t, listener.ListenerFilters, 1)
	assert.Contains(t, listener.ListenerFilters[0].Name, "tls_inspector")
	require.Len(t, listener.FilterChains, 3)

	for i, route := range routes {
		fc := listener.FilterChains[i]
		require.NotNil(t, fc.FilterChainMatch)
		assert.Equal(t, []string{route.SNIHostname}, fc.FilterChainMatch.ServerNames)
		assert.NotNil(t, fc.TransportSocket, "downstream TLS should be present")
	}
}

func TestBuildLDSJSON_ReplicaSet_NoTLS(t *testing.T) {
	route := envoyRoute{
		Name: "rs", NameSafe: "rs",
		SNIHostname:   "mdb-search-search-proxy-svc.test-ns.svc.cluster.local",
		UpstreamHosts: []string{"mdb-search-search-svc.test-ns.svc.cluster.local"},
		UpstreamPort:  27028,
	}

	result, err := buildLDSJSON([]envoyRoute{route}, false, testCAKeyName(), nil)
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	listener := &listenerv3.Listener{}
	err = resp.Resources[0].UnmarshalTo(listener)
	require.NoError(t, err)

	assert.Empty(t, listener.ListenerFilters, "no TLS Inspector for non-TLS RS")
	require.Len(t, listener.FilterChains, 1)
	assert.Nil(t, listener.FilterChains[0].FilterChainMatch, "no SNI match for non-TLS RS")
	assert.Nil(t, listener.FilterChains[0].TransportSocket, "no downstream TLS for non-TLS RS")
}

func TestBuildCDSJSON_ReplicaSet_NoTLS(t *testing.T) {
	route := envoyRoute{
		Name: "rs", NameSafe: "rs",
		SNIHostname:   "mdb-search-search-proxy-svc.test-ns.svc.cluster.local",
		UpstreamHosts: []string{"mdb-search-search-svc.test-ns.svc.cluster.local"},
		UpstreamPort:  27028,
	}

	result, err := buildCDSJSON([]envoyRoute{route}, false, testCAKeyName())
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	require.Len(t, resp.Resources, 1)

	cluster := &clusterv3.Cluster{}
	err = resp.Resources[0].UnmarshalTo(cluster)
	require.NoError(t, err)
	assert.Equal(t, "mongot_rs_cluster", cluster.Name)
	assert.Nil(t, cluster.TransportSocket, "no upstream TLS for non-TLS RS")

	ep := cluster.LoadAssignment.Endpoints[0].LbEndpoints[0].GetEndpoint()
	assert.Equal(t, route.UpstreamHosts[0], ep.Address.GetSocketAddress().GetAddress())
	assert.Equal(t, uint32(route.UpstreamPort), ep.Address.GetSocketAddress().GetPortValue())
}

func TestBuildFilterChain_NoTLS_NoSNIMatch(t *testing.T) {
	route := testRoute("test-shard")
	chain, err := buildFilterChain(route, false, testCAKeyName(), nil)
	require.NoError(t, err)

	assert.Nil(t, chain.FilterChainMatch, "no SNI match when TLS disabled")
	assert.Nil(t, chain.TransportSocket, "no transport socket when TLS disabled")
	require.Len(t, chain.Filters, 1, "HCM filter should be present")
}

func TestBuildFilterChain_WithTLS_HasSNIMatch(t *testing.T) {
	route := testRoute("test-shard")
	chain, err := buildFilterChain(route, true, testCAKeyName(), nil)
	require.NoError(t, err)

	require.NotNil(t, chain.FilterChainMatch, "SNI match should be present with TLS")
	assert.Equal(t, []string{route.SNIHostname}, chain.FilterChainMatch.ServerNames)
	assert.NotNil(t, chain.TransportSocket, "transport socket should be present with TLS")
}

func TestBuildLDSJSON_NoTLS_NoTLSInspector(t *testing.T) {
	route := testRoute("test-shard")
	result, err := buildLDSJSON([]envoyRoute{route}, false, testCAKeyName(), nil)
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	listener := &listenerv3.Listener{}
	err = resp.Resources[0].UnmarshalTo(listener)
	require.NoError(t, err)
	assert.Empty(t, listener.ListenerFilters, "no listener filters when TLS disabled")
}

func TestBuildLDSJSON_WithTLS_HasTLSInspector(t *testing.T) {
	route := testRoute("test-shard")
	result, err := buildLDSJSON([]envoyRoute{route}, true, testCAKeyName(), nil)
	require.NoError(t, err)

	resp := unmarshalDiscoveryResponse(t, result)
	listener := &listenerv3.Listener{}
	err = resp.Resources[0].UnmarshalTo(listener)
	require.NoError(t, err)
	require.Len(t, listener.ListenerFilters, 1)
	assert.Contains(t, listener.ListenerFilters[0].Name, "tls_inspector")
}

func TestBuildUpstreamTLS_ClusterLevelRoute_EmptySNI(t *testing.T) {
	clusterRoute := envoyRoute{
		Name:          "cluster-level",
		NameSafe:      "cluster_level",
		SNIHostname:   "cluster-level-proxy.ns.svc.cluster.local",
		UpstreamHosts: []string{"mongot0.ns.svc.cluster.local", "mongot1.ns.svc.cluster.local"},
		UpstreamPort:  27028,
	}

	ts, err := buildUpstreamTLSTransportSocket(clusterRoute, testCAKeyName())
	require.NoError(t, err)

	upstreamTLS := &tlsv3.UpstreamTlsContext{}
	err = ts.GetTypedConfig().UnmarshalTo(upstreamTLS)
	require.NoError(t, err)
	assert.Empty(t, upstreamTLS.Sni, "cluster-level route must have empty SNI")
}

func TestBuildUpstreamTLS_PerShardRoute_SNISet(t *testing.T) {
	route := testRoute("mdb-sh-0")

	ts, err := buildUpstreamTLSTransportSocket(route, testCAKeyName())
	require.NoError(t, err)

	upstreamTLS := &tlsv3.UpstreamTlsContext{}
	err = ts.GetTypedConfig().UnmarshalTo(upstreamTLS)
	require.NoError(t, err)
	assert.Equal(t, route.UpstreamHosts[0], upstreamTLS.Sni)
}

func TestBuildCluster_UsesTypedExtensionProtocolOptions(t *testing.T) {
	route := testRoute("mdb-sh-0")
	cluster, err := buildCluster(route, false, testCAKeyName())
	require.NoError(t, err)

	// Verify deprecated fields are NOT set
	assert.Nil(t, cluster.Http2ProtocolOptions, "deprecated Http2ProtocolOptions should not be set on Cluster")           //nolint:staticcheck
	assert.Nil(t, cluster.CommonHttpProtocolOptions, "deprecated CommonHttpProtocolOptions should not be set on Cluster") //nolint:staticcheck

	// Verify TypedExtensionProtocolOptions is set
	require.Contains(t, cluster.TypedExtensionProtocolOptions, "envoy.extensions.upstreams.http.v3.HttpProtocolOptions")
}

func TestBuildRetryPolicy_PartialOverride(t *testing.T) {
	numRetries := uint32(5)
	rp := buildRetryPolicy(&searchv1.EnvoyRetryPolicy{
		NumRetries: &numRetries,
		// PerTryTimeout left nil — should use default 60s
	})

	assert.Equal(t, uint32(5), rp.NumRetries.GetValue())
	assert.Equal(t, int64(60), rp.PerTryTimeout.GetSeconds(), "should use default timeout")
}

func TestBuildFilterChain_HasRetryPolicy(t *testing.T) {
	route := testRoute("mdb-sh-0")
	chain, err := buildFilterChain(route, false, testCAKeyName(), nil)
	require.NoError(t, err)

	// Extract HCM from the filter chain
	hcm := &hcmv3.HttpConnectionManager{}
	err = chain.Filters[0].GetTypedConfig().UnmarshalTo(hcm)
	require.NoError(t, err)

	// Get the retry policy from the route action
	routeConfig := hcm.GetRouteConfig()
	require.NotNil(t, routeConfig)
	require.Len(t, routeConfig.VirtualHosts, 1)

	vh := routeConfig.VirtualHosts[0]
	assert.True(t, vh.IncludeRequestAttemptCount, "should include request attempt count in config so that it can be set in header")

	require.Len(t, vh.Routes, 1)
	routeAction := vh.Routes[0].GetRoute()
	require.NotNil(t, routeAction)
	require.NotNil(t, routeAction.RetryPolicy)

	rp := routeAction.RetryPolicy
	assert.Equal(t, "connect-failure,refused-stream,unavailable,reset,resource-exhausted", rp.RetryOn)
	assert.Equal(t, uint32(2), rp.NumRetries.GetValue())
	assert.Equal(t, int64(60), rp.PerTryTimeout.GetSeconds())
	require.Len(t, rp.RetryHostPredicate, 1)
	assert.Equal(t, "envoy.retry_host_predicates.previous_hosts", rp.RetryHostPredicate[0].Name)
	assert.Equal(t, int64(3), rp.HostSelectionRetryMaxAttempts)
}

func TestBuildFilterChain_DisablesRouteClusterValidation(t *testing.T) {
	route := testRoute("mdb-sh-0")
	chain, err := buildFilterChain(route, false, testCAKeyName(), nil)
	require.NoError(t, err)

	hcm := &hcmv3.HttpConnectionManager{}
	err = chain.Filters[0].GetTypedConfig().UnmarshalTo(hcm)
	require.NoError(t, err)

	routeConfig := hcm.GetRouteConfig()
	require.NotNil(t, routeConfig)
	// Must be explicitly false: with the default (true for listener-inlined routes) Envoy
	// rejects the whole LDS update when lds.json is read before cds.json adds a new
	// shard's cluster, and never retries — wedging the new shard's $search path.
	require.NotNil(t, routeConfig.ValidateClusters)
	assert.False(t, routeConfig.ValidateClusters.GetValue())
}

func TestBuildFilterChain_RoutedFromAnotherShard(t *testing.T) {
	route := testRoute("mdb-sh-2")
	route.RoutedFromAnotherShard = true

	chain, err := buildFilterChain(route, false, testCAKeyName(), nil)
	require.NoError(t, err)

	// Extract HCM from the filter chain
	hcm := &hcmv3.HttpConnectionManager{}
	err = chain.Filters[0].GetTypedConfig().UnmarshalTo(hcm)
	require.NoError(t, err)

	// Route should point to the cluster-level cluster, not the per-shard cluster
	routes := hcm.GetRouteConfig().GetVirtualHosts()[0].GetRoutes()
	require.Len(t, routes, 1)
	assert.Equal(t, "mongot_cluster_level_cluster", routes[0].GetRoute().GetCluster())

	// Should have the search-envoy-metadata-bin header injected
	headersToAdd := routes[0].GetRequestHeadersToAdd()
	require.Len(t, headersToAdd, 1)
	assert.Equal(t, searchEnvoyMetadataHeader, headersToAdd[0].GetHeader().GetKey())
	assert.Equal(t, searchEnvoyMetadataRoutedValue, headersToAdd[0].GetHeader().GetValue())
	assert.Equal(t, corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD, headersToAdd[0].GetAppendAction())
}

func TestBuildFilterChain_NormalRoute_NoHeader(t *testing.T) {
	route := testRoute("mdb-sh-0")
	route.RoutedFromAnotherShard = false

	chain, err := buildFilterChain(route, false, testCAKeyName(), nil)
	require.NoError(t, err)

	// Extract HCM from the filter chain
	hcm := &hcmv3.HttpConnectionManager{}
	err = chain.Filters[0].GetTypedConfig().UnmarshalTo(hcm)
	require.NoError(t, err)

	// Route should point to per-shard cluster
	routes := hcm.GetRouteConfig().GetVirtualHosts()[0].GetRoutes()
	require.Len(t, routes, 1)
	assert.Equal(t, "mongot_mdb_sh_0_cluster", routes[0].GetRoute().GetCluster())

	// Should NOT have any headers injected
	assert.Empty(t, routes[0].GetRequestHeadersToAdd())
}
