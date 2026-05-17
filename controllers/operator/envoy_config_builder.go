package operator

import (
	"fmt"
	"time"

	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"

	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	stdoutaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/stream/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	tlsinspectorv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/tls_inspector/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	upstreamhttpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
)

// buildEnvoyConfigJSON builds the Envoy bootstrap configuration using
// go-control-plane protobuf types and marshals it to JSON.
func buildEnvoyConfigJSON(routes []envoyRoute, tlsEnabled bool, caKeyName string) (string, error) {
	config, err := buildEnvoyBootstrapConfig(routes, tlsEnabled, caKeyName)
	if err != nil {
		return "", fmt.Errorf("failed to build Envoy bootstrap config: %w", err)
	}

	marshaler := protojson.MarshalOptions{
		UseProtoNames: true, // snake_case field names (matches Envoy expectations)
		Indent:        "  ",
	}
	data, err := marshaler.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Envoy config to JSON: %w", err)
	}

	return string(data), nil
}

// buildEnvoyBootstrapConfig constructs the full Envoy bootstrap protobuf.
func buildEnvoyBootstrapConfig(routes []envoyRoute, tlsEnabled bool, caKeyName string) (*bootstrapv3.Bootstrap, error) {
	filterChains := make([]*listenerv3.FilterChain, 0, len(routes))
	clusters := make([]*clusterv3.Cluster, 0, len(routes))

	for _, route := range routes {
		fc, err := buildFilterChain(route, tlsEnabled, caKeyName)
		if err != nil {
			return nil, fmt.Errorf("failed to build filter chain for route %s: %w", route.Name, err)
		}
		filterChains = append(filterChains, fc)

		cl, err := buildCluster(route, tlsEnabled, caKeyName)
		if err != nil {
			return nil, fmt.Errorf("failed to build cluster for route %s: %w", route.Name, err)
		}
		clusters = append(clusters, cl)
	}

	var listenerFilters []*listenerv3.ListenerFilter
	if tlsEnabled {
		tlsInspectorCfg, err := anypb.New(&tlsinspectorv3.TlsInspector{})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal TLS inspector config: %w", err)
		}
		listenerFilters = []*listenerv3.ListenerFilter{
			{
				Name: wellknown.TLSInspector,
				ConfigType: &listenerv3.ListenerFilter_TypedConfig{
					TypedConfig: tlsInspectorCfg,
				},
			},
		}
	}

	runtimeStruct, err := structpb.NewStruct(map[string]interface{}{
		"overload": map[string]interface{}{
			"global_downstream_max_connections": 50000,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build runtime struct: %w", err)
	}

	return &bootstrapv3.Bootstrap{
		Admin: &bootstrapv3.Admin{
			Address: socketAddress("0.0.0.0", uint32(envoyAdminPort)),
		},
		StaticResources: &bootstrapv3.Bootstrap_StaticResources{
			Listeners: []*listenerv3.Listener{
				{
					Name:            "mongod_listener",
					Address:         socketAddress("0.0.0.0", uint32(searchv1.EnvoyDefaultProxyPort)),
					ListenerFilters: listenerFilters,
					FilterChains:    filterChains,
				},
			},
			Clusters: clusters,
		},
		LayeredRuntime: &bootstrapv3.LayeredRuntime{
			Layers: []*bootstrapv3.RuntimeLayer{
				{
					Name: "static_layer",
					LayerSpecifier: &bootstrapv3.RuntimeLayer_StaticLayer{
						StaticLayer: runtimeStruct,
					},
				},
			},
		},
	}, nil
}

// socketAddress builds an Envoy Address with a TCP socket.
func socketAddress(addr string, port uint32) *corev3.Address {
	return &corev3.Address{
		Address: &corev3.Address_SocketAddress{
			SocketAddress: &corev3.SocketAddress{
				Address:       addr,
				PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: port},
			},
		},
	}
}

// buildFilterChain builds a filter chain for one route.
func buildFilterChain(route envoyRoute, tlsEnabled bool, caKeyName string) (*listenerv3.FilterChain, error) {
	clusterName := fmt.Sprintf("mongot_%s_cluster", route.NameSafe)

	routerFilterCfg, err := anypb.New(&routerv3.Router{})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal router filter config: %w", err)
	}

	accessLog, err := buildHCMAccessLog()
	if err != nil {
		return nil, err
	}

	hcm := &hcmv3.HttpConnectionManager{
		StatPrefix: fmt.Sprintf("ingress_%s", route.NameSafe),
		CodecType:  hcmv3.HttpConnectionManager_AUTO,
		RouteSpecifier: &hcmv3.HttpConnectionManager_RouteConfig{
			RouteConfig: &routev3.RouteConfiguration{
				Name: fmt.Sprintf("%s_route", route.NameSafe),
				VirtualHosts: []*routev3.VirtualHost{
					{
						Name:    fmt.Sprintf("mongot_%s_backend", route.NameSafe),
						Domains: []string{"*"},
						Routes: []*routev3.Route{
							{
								Match: &routev3.RouteMatch{
									PathSpecifier: &routev3.RouteMatch_Prefix{Prefix: "/"},
									Grpc:          &routev3.RouteMatch_GrpcRouteMatchOptions{},
								},
								Action: &routev3.Route_Route{
									Route: &routev3.RouteAction{
										ClusterSpecifier: &routev3.RouteAction_Cluster{Cluster: clusterName},
										Timeout:          durationpb.New(300 * time.Second),
									},
								},
							},
						},
					},
				},
			},
		},
		HttpFilters: []*hcmv3.HttpFilter{
			{
				Name:       wellknown.Router,
				ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: routerFilterCfg},
			},
		},
		Http2ProtocolOptions: &corev3.Http2ProtocolOptions{
			InitialConnectionWindowSize: wrapperspb.UInt32(1048576),
			InitialStreamWindowSize:     wrapperspb.UInt32(1048576),
		},
		StreamIdleTimeout: durationpb.New(300 * time.Second),
		RequestTimeout:    durationpb.New(300 * time.Second),
		AccessLog:         accessLog,
	}

	hcmAny, err := anypb.New(hcm)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal HCM config: %w", err)
	}

	chain := &listenerv3.FilterChain{
		Filters: []*listenerv3.Filter{
			{
				Name:       wellknown.HTTPConnectionManager,
				ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny},
			},
		},
	}

	if tlsEnabled {
		chain.FilterChainMatch = &listenerv3.FilterChainMatch{
			ServerNames: []string{route.SNIHostname},
		}
		ts, err := buildDownstreamTLSTransportSocket(caKeyName)
		if err != nil {
			return nil, err
		}
		chain.TransportSocket = ts
	}

	return chain, nil
}

// buildCluster builds an upstream cluster for one route.
func buildCluster(route envoyRoute, tlsEnabled bool, caKeyName string) (*clusterv3.Cluster, error) {
	clusterName := fmt.Sprintf("mongot_%s_cluster", route.NameSafe)

	upstreamHTTPOpts, err := anypb.New(&upstreamhttpv3.HttpProtocolOptions{
		CommonHttpProtocolOptions: &corev3.HttpProtocolOptions{
			IdleTimeout: durationpb.New(300 * time.Second),
		},
		UpstreamProtocolOptions: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_{
			ExplicitHttpConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig{
				ProtocolConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{
					Http2ProtocolOptions: &corev3.Http2ProtocolOptions{
						InitialConnectionWindowSize: wrapperspb.UInt32(1048576),
						InitialStreamWindowSize:     wrapperspb.UInt32(1048576),
					},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upstream HTTP protocol options: %w", err)
	}

	cluster := &clusterv3.Cluster{
		Name:                 clusterName,
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STRICT_DNS},
		LbPolicy:             clusterv3.Cluster_ROUND_ROBIN,
		TypedExtensionProtocolOptions: map[string]*anypb.Any{
			"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": upstreamHTTPOpts,
		},
		LoadAssignment: &endpointv3.ClusterLoadAssignment{
			ClusterName: clusterName,
			Endpoints: []*endpointv3.LocalityLbEndpoints{
				{
					LbEndpoints: []*endpointv3.LbEndpoint{
						{
							HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
								Endpoint: &endpointv3.Endpoint{
									Address: socketAddress(route.UpstreamHost, uint32(route.UpstreamPort)), //nolint:gosec
								},
							},
						},
					},
				},
			},
		},
		CircuitBreakers: &clusterv3.CircuitBreakers{
			Thresholds: []*clusterv3.CircuitBreakers_Thresholds{
				{
					Priority:           corev3.RoutingPriority_DEFAULT,
					MaxConnections:     wrapperspb.UInt32(1024),
					MaxPendingRequests: wrapperspb.UInt32(1024),
					MaxRequests:        wrapperspb.UInt32(1024),
					MaxRetries:         wrapperspb.UInt32(3),
				},
			},
		},
		UpstreamConnectionOptions: &clusterv3.UpstreamConnectionOptions{
			TcpKeepalive: &corev3.TcpKeepalive{
				KeepaliveTime:     wrapperspb.UInt32(10),
				KeepaliveInterval: wrapperspb.UInt32(3),
				KeepaliveProbes:   wrapperspb.UInt32(3),
			},
		},
	}

	if tlsEnabled {
		ts, err := buildUpstreamTLSTransportSocket(route, caKeyName)
		if err != nil {
			return nil, err
		}
		cluster.TransportSocket = ts
	}

	return cluster, nil
}

// buildDownstreamTLSTransportSocket builds the TLS transport socket for the listener (downstream).
func buildDownstreamTLSTransportSocket(caKeyName string) (*corev3.TransportSocket, error) {
	downstreamTLS := &tlsv3.DownstreamTlsContext{
		CommonTlsContext: &tlsv3.CommonTlsContext{
			TlsCertificates: []*tlsv3.TlsCertificate{
				{
					CertificateChain: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyServerCertPath + "/tls.crt"},
					},
					PrivateKey: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyServerCertPath + "/tls.key"},
					},
				},
			},
			ValidationContextType: &tlsv3.CommonTlsContext_ValidationContext{
				ValidationContext: &tlsv3.CertificateValidationContext{
					TrustedCa: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyCACertPath + "/" + caKeyName},
					},
				},
			},
			TlsParams: &tlsv3.TlsParameters{
				TlsMinimumProtocolVersion: tlsv3.TlsParameters_TLSv1_3,
				TlsMaximumProtocolVersion: tlsv3.TlsParameters_TLSv1_3,
			},
			AlpnProtocols: []string{"h2"},
		},
		RequireClientCertificate: wrapperspb.Bool(true),
	}

	tlsAny, err := anypb.New(downstreamTLS)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal downstream TLS context: %w", err)
	}

	return &corev3.TransportSocket{
		Name:       wellknown.TransportSocketTLS,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: tlsAny},
	}, nil
}

// buildHCMAccessLog returns the HCM access_log entries that emit one
// JSON-formatted line to stdout per HTTP/HTTP2 stream close.
//
// Envoy emits exactly ONE access-log record per gRPC bidi stream — at
// stream close — unless per-frame access logging is opted into (out of
// scope for failure-mode tests). The single close record still captures
// the load-bearing signals we need cross-side:
//
//   - %REQ(MONGODB-CLIENTID)% — the UUID the mongodb client puts on the
//     request header. The exact same UUID mongod logs as
//     attr.session.clientId at network:2 (id=7401401), giving us a
//     deterministic envoy ↔ mongod join key without any time tolerance.
//   - %RESPONSE_FLAGS% — envoy's encoded response disposition; surfaces
//     UF/UR/etc when a mongot pod dies mid-stream so failure-mode tests
//     can assert on the envoy-side signal in addition to mongod's
//     "Remote error from mongot" / "RST_STREAM" surface error.
//   - %UPSTREAM_HOST% — which mongot endpoint envoy actually picked.
//   - %BYTES_RECEIVED% / %BYTES_SENT% / %DURATION% — basic per-stream
//     traffic stats for the post-hoc test report.
//
// JSON formatting keeps the per-line schema stable across envoy releases
// and is easier for the analyzer's parser to consume than the default
// text format.
func buildHCMAccessLog() ([]*accesslogv3.AccessLog, error) {
	jsonFields, err := structpb.NewStruct(map[string]interface{}{
		"ts":             "%START_TIME%",
		"duration_ms":    "%DURATION%",
		"response_flags": "%RESPONSE_FLAGS%",
		"response_code":  "%RESPONSE_CODE%",
		"grpc_status":    "%GRPC_STATUS%",
		"upstream_host":  "%UPSTREAM_HOST%",
		"bytes_in":       "%BYTES_RECEIVED%",
		"bytes_out":      "%BYTES_SENT%",
		"client_id":      "%REQ(MONGODB-CLIENTID)%",
		"path":           "%REQ(:PATH)%",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build access-log json format struct: %w", err)
	}

	stdoutLog := &stdoutaccesslogv3.StdoutAccessLog{
		AccessLogFormat: &stdoutaccesslogv3.StdoutAccessLog_LogFormat{
			LogFormat: &corev3.SubstitutionFormatString{
				Format: &corev3.SubstitutionFormatString_JsonFormat{
					JsonFormat: jsonFields,
				},
			},
		},
	}
	stdoutAny, err := anypb.New(stdoutLog)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal stdout access-log config: %w", err)
	}

	return []*accesslogv3.AccessLog{
		{
			Name: "envoy.access_loggers.stdout",
			ConfigType: &accesslogv3.AccessLog_TypedConfig{
				TypedConfig: stdoutAny,
			},
		},
	}, nil
}

// buildUpstreamTLSTransportSocket builds the TLS transport socket for clusters (upstream).
func buildUpstreamTLSTransportSocket(route envoyRoute, caKeyName string) (*corev3.TransportSocket, error) {
	upstreamTLS := &tlsv3.UpstreamTlsContext{
		CommonTlsContext: &tlsv3.CommonTlsContext{
			TlsCertificates: []*tlsv3.TlsCertificate{
				{
					CertificateChain: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyClientCertPath + "/tls.crt"},
					},
					PrivateKey: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyClientCertPath + "/tls.key"},
					},
				},
			},
			ValidationContextType: &tlsv3.CommonTlsContext_ValidationContext{
				ValidationContext: &tlsv3.CertificateValidationContext{
					TrustedCa: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyCACertPath + "/" + caKeyName},
					},
				},
			},
			AlpnProtocols: []string{"h2"},
		},
		Sni: route.UpstreamHost,
	}

	tlsAny, err := anypb.New(upstreamTLS)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upstream TLS context: %w", err)
	}

	return &corev3.TransportSocket{
		Name:       wellknown.TransportSocketTLS,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: tlsAny},
	}, nil
}
