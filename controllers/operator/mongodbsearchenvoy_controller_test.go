package operator

import (
	"encoding/json"
	"testing"

	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func testRoute(shardName string) shardRoute {
	return shardRoute{
		ShardName:     shardName,
		ShardNameSafe: "mdb_sh_0",
		SNIHostname:   shardName + "-proxy.ns.svc.cluster.local",
		UpstreamHost:  shardName + "-mongot.ns.svc.cluster.local",
		UpstreamPort:  27028,
	}
}

func testCA() caConfig {
	return caConfig{
		ConfigMapName: "test-ca",
		Key:           "ca-pem",
	}
}

func unmarshalBootstrap(t *testing.T, jsonStr string) *bootstrapv3.Bootstrap {
	t.Helper()
	bootstrap := &bootstrapv3.Bootstrap{}
	err := protojson.Unmarshal([]byte(jsonStr), bootstrap)
	require.NoError(t, err, "failed to unmarshal bootstrap config")
	return bootstrap
}

func TestBuildEnvoyConfigJSON_OutputIsValidJSON(t *testing.T) {
	result, err := buildEnvoyConfigJSON([]shardRoute{testRoute("mdb-sh-0")}, false, testCA())
	require.NoError(t, err)
	assert.True(t, json.Valid([]byte(result)), "output should be valid JSON")
}

func TestBuildEnvoyConfigJSON_SingleShard_NoTLS(t *testing.T) {
	route := testRoute("mdb-sh-0")
	result, err := buildEnvoyConfigJSON([]shardRoute{route}, false, testCA())
	require.NoError(t, err)

	bootstrap := unmarshalBootstrap(t, result)

	// Admin
	require.NotNil(t, bootstrap.Admin)
	adminAddr := bootstrap.Admin.Address.GetSocketAddress()
	assert.Equal(t, "0.0.0.0", adminAddr.GetAddress())
	assert.Equal(t, uint32(envoyAdminPort), adminAddr.GetPortValue())

	// Static resources
	require.NotNil(t, bootstrap.StaticResources)
	require.Len(t, bootstrap.StaticResources.Listeners, 1)
	listener := bootstrap.StaticResources.Listeners[0]
	assert.Equal(t, "mongod_listener", listener.Name)
	assert.Equal(t, uint32(envoyProxyPort), listener.Address.GetSocketAddress().GetPortValue())

	// Listener filter (TLS Inspector)
	require.Len(t, listener.ListenerFilters, 1)
	assert.Contains(t, listener.ListenerFilters[0].Name, "tls_inspector")

	// Filter chains
	require.Len(t, listener.FilterChains, 1)
	fc := listener.FilterChains[0]
	assert.Equal(t, []string{route.SNIHostname}, fc.FilterChainMatch.ServerNames)
	assert.Nil(t, fc.TransportSocket, "no TLS transport socket when TLS disabled")

	// Clusters
	require.Len(t, bootstrap.StaticResources.Clusters, 1)
	cluster := bootstrap.StaticResources.Clusters[0]
	assert.Equal(t, "mongot_mdb_sh_0_cluster", cluster.Name)
	assert.Equal(t, clusterv3.Cluster_STRICT_DNS, cluster.GetType())
	assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
	assert.Nil(t, cluster.TransportSocket, "no TLS transport socket when TLS disabled")

	// Endpoint
	require.Len(t, cluster.LoadAssignment.Endpoints, 1)
	require.Len(t, cluster.LoadAssignment.Endpoints[0].LbEndpoints, 1)
	ep := cluster.LoadAssignment.Endpoints[0].LbEndpoints[0].GetEndpoint()
	assert.Equal(t, route.UpstreamHost, ep.Address.GetSocketAddress().GetAddress())
	assert.Equal(t, uint32(route.UpstreamPort), ep.Address.GetSocketAddress().GetPortValue())

	// Circuit breakers
	require.NotNil(t, cluster.CircuitBreakers)
	require.Len(t, cluster.CircuitBreakers.Thresholds, 1)
	thresh := cluster.CircuitBreakers.Thresholds[0]
	assert.Equal(t, corev3.RoutingPriority_DEFAULT, thresh.Priority)
	assert.Equal(t, uint32(1024), thresh.MaxConnections.GetValue())

	// TCP keepalive
	require.NotNil(t, cluster.UpstreamConnectionOptions)
	require.NotNil(t, cluster.UpstreamConnectionOptions.TcpKeepalive)
	assert.Equal(t, uint32(10), cluster.UpstreamConnectionOptions.TcpKeepalive.KeepaliveTime.GetValue())

	// LayeredRuntime
	require.NotNil(t, bootstrap.LayeredRuntime)
	require.Len(t, bootstrap.LayeredRuntime.Layers, 1)
	assert.Equal(t, "static_layer", bootstrap.LayeredRuntime.Layers[0].Name)
}

func TestBuildEnvoyConfigJSON_SingleShard_WithTLS(t *testing.T) {
	route := testRoute("mdb-sh-0")
	ca := testCA()
	result, err := buildEnvoyConfigJSON([]shardRoute{route}, true, ca)
	require.NoError(t, err)

	bootstrap := unmarshalBootstrap(t, result)

	// Filter chain has downstream TLS
	fc := bootstrap.StaticResources.Listeners[0].FilterChains[0]
	require.NotNil(t, fc.TransportSocket, "TLS transport socket should be present")

	downstreamTLS := &tlsv3.DownstreamTlsContext{}
	err = fc.TransportSocket.GetTypedConfig().UnmarshalTo(downstreamTLS)
	require.NoError(t, err)

	assert.True(t, downstreamTLS.RequireClientCertificate.GetValue())
	assert.Equal(t, tlsv3.TlsParameters_TLSv1_2, downstreamTLS.CommonTlsContext.TlsParams.TlsMinimumProtocolVersion)
	assert.Equal(t, tlsv3.TlsParameters_TLSv1_2, downstreamTLS.CommonTlsContext.TlsParams.TlsMaximumProtocolVersion)
	assert.Equal(t, []string{"h2"}, downstreamTLS.CommonTlsContext.AlpnProtocols)

	require.Len(t, downstreamTLS.CommonTlsContext.TlsCertificates, 1)
	cert := downstreamTLS.CommonTlsContext.TlsCertificates[0]
	assert.Equal(t, envoyServerCertPath+"/tls.crt", cert.CertificateChain.GetFilename())
	assert.Equal(t, envoyServerCertPath+"/tls.key", cert.PrivateKey.GetFilename())

	valCtx := downstreamTLS.CommonTlsContext.GetValidationContext()
	require.NotNil(t, valCtx)
	assert.Equal(t, envoyCACertPath+"/"+ca.Key, valCtx.TrustedCa.GetFilename())

	// Cluster has upstream TLS
	cluster := bootstrap.StaticResources.Clusters[0]
	require.NotNil(t, cluster.TransportSocket, "upstream TLS transport socket should be present")

	upstreamTLS := &tlsv3.UpstreamTlsContext{}
	err = cluster.TransportSocket.GetTypedConfig().UnmarshalTo(upstreamTLS)
	require.NoError(t, err)

	assert.Equal(t, route.UpstreamHost, upstreamTLS.Sni)
	assert.Equal(t, []string{"h2"}, upstreamTLS.CommonTlsContext.AlpnProtocols)

	require.Len(t, upstreamTLS.CommonTlsContext.TlsCertificates, 1)
	clientCert := upstreamTLS.CommonTlsContext.TlsCertificates[0]
	assert.Equal(t, envoyClientCertPath+"/tls.crt", clientCert.CertificateChain.GetFilename())
	assert.Equal(t, envoyClientCertPath+"/tls.key", clientCert.PrivateKey.GetFilename())
}

func TestBuildEnvoyConfigJSON_MultipleShards(t *testing.T) {
	routes := []shardRoute{
		{ShardName: "mdb-sh-0", ShardNameSafe: "mdb_sh_0", SNIHostname: "shard0.ns.svc.cluster.local", UpstreamHost: "mongot0.ns.svc.cluster.local", UpstreamPort: 27028},
		{ShardName: "mdb-sh-1", ShardNameSafe: "mdb_sh_1", SNIHostname: "shard1.ns.svc.cluster.local", UpstreamHost: "mongot1.ns.svc.cluster.local", UpstreamPort: 27028},
		{ShardName: "mdb-sh-2", ShardNameSafe: "mdb_sh_2", SNIHostname: "shard2.ns.svc.cluster.local", UpstreamHost: "mongot2.ns.svc.cluster.local", UpstreamPort: 27028},
	}

	result, err := buildEnvoyConfigJSON(routes, false, testCA())
	require.NoError(t, err)

	bootstrap := unmarshalBootstrap(t, result)

	require.Len(t, bootstrap.StaticResources.Listeners[0].FilterChains, 3)
	require.Len(t, bootstrap.StaticResources.Clusters, 3)

	for i, route := range routes {
		fc := bootstrap.StaticResources.Listeners[0].FilterChains[i]
		assert.Equal(t, []string{route.SNIHostname}, fc.FilterChainMatch.ServerNames)

		cluster := bootstrap.StaticResources.Clusters[i]
		expectedName := "mongot_" + route.ShardNameSafe + "_cluster"
		assert.Equal(t, expectedName, cluster.Name)

		ep := cluster.LoadAssignment.Endpoints[0].LbEndpoints[0].GetEndpoint()
		assert.Equal(t, route.UpstreamHost, ep.Address.GetSocketAddress().GetAddress())
	}
}

func TestBuildCluster_UsesTypedExtensionProtocolOptions(t *testing.T) {
	route := testRoute("mdb-sh-0")
	cluster, err := buildCluster(route, false, testCA())
	require.NoError(t, err)

	// Verify deprecated fields are NOT set
	assert.Nil(t, cluster.Http2ProtocolOptions, "deprecated Http2ProtocolOptions should not be set on Cluster")
	assert.Nil(t, cluster.CommonHttpProtocolOptions, "deprecated CommonHttpProtocolOptions should not be set on Cluster")

	// Verify TypedExtensionProtocolOptions is set
	require.Contains(t, cluster.TypedExtensionProtocolOptions, "envoy.extensions.upstreams.http.v3.HttpProtocolOptions")
}
