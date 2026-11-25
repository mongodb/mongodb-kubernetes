package operator

import (
	"bytes"
	"fmt"
	"sort"
	"text/template"

	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	_ "embed"

	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	bufferv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/buffer/v3"
	jsonmetadatav3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/json_to_metadata/v3"
	luav3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/lua/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	upstreamhttpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	matcherv3 "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"

	vaiv1 "github.com/mongodb/mongodb-kubernetes/api/voyageai/v1/vai"
)

const (
	voyageRouterConfigPath        = "/etc/voyage-router"
	voyageRouterInboundTLSPath    = "/etc/voyage-router/inbound-tls"
	voyageRouterUpstreamTLSPath   = "/etc/voyage-router/upstream-ca"
	voyageRouterInternalHeader    = "x-mongodb-voyage-router-cluster"
	voyageRouterMetadataNamespace = "envoy.filters.http.json_to_metadata"
	voyageRouterListenerPort      = 8080
	voyageRouterUpstreamCAFile    = "ca.crt"
)

type voyageRouterBackend struct {
	ServiceFQDN string
	Port        int32
	TLS         bool
}

type voyageRouterModel struct {
	Model    vaiv1.VoyageAIModel
	Selected bool
	Backends []voyageRouterBackend
}

type voyageRouterLuaTemplateModel struct {
	Name    string
	Cluster string
}

type voyageRouterLuaTemplateData struct {
	Models            []voyageRouterLuaTemplateModel
	Paths             []string
	InternalHeader    string
	MetadataNamespace string
}

//go:embed voyagerouter_envoy.lua.tmpl
var voyageRouterLuaTemplateSource string

var voyageRouterLuaTemplate = template.Must(
	template.New("voyage_router_envoy_lua").Parse(voyageRouterLuaTemplateSource),
)

func voyageRouterClusterName(model vaiv1.VoyageAIModel) string {
	return "voyage_router_" + string(model)
}

func buildVoyageRouterBootstrapJSON() (string, error) {
	pathConfigSource := func(path string) *corev3.ConfigSource {
		return &corev3.ConfigSource{
			ConfigSourceSpecifier: &corev3.ConfigSource_PathConfigSource{
				PathConfigSource: &corev3.PathConfigSource{
					Path:             path,
					WatchedDirectory: &corev3.WatchedDirectory{Path: voyageRouterConfigPath},
				},
			},
		}
	}

	return marshalJSON(&bootstrapv3.Bootstrap{
		Node: &corev3.Node{Id: "envoy-voyage-router", Cluster: "voyage-router"},
		Admin: &bootstrapv3.Admin{
			Address: socketAddress("0.0.0.0", uint32(EnvoyAdminPort)),
			AllowPaths: []*matcherv3.StringMatcher{
				{MatchPattern: &matcherv3.StringMatcher_Exact{Exact: "/ready"}},
				{MatchPattern: &matcherv3.StringMatcher_Prefix{Prefix: "/stats"}},
				{MatchPattern: &matcherv3.StringMatcher_Prefix{Prefix: "/drain_listeners"}},
				{MatchPattern: &matcherv3.StringMatcher_Prefix{Prefix: "/logging"}},
			},
		},
		DynamicResources: &bootstrapv3.Bootstrap_DynamicResources{
			CdsConfig: pathConfigSource(voyageRouterConfigPath + "/cds.json"),
			LdsConfig: pathConfigSource(voyageRouterConfigPath + "/lds.json"),
		},
	})
}

func buildVoyageRouterCDSJSON(models []voyageRouterModel) (string, error) {
	byModel := make(map[vaiv1.VoyageAIModel]voyageRouterModel, len(models))
	for _, model := range models {
		byModel[model.Model] = model
	}

	resources := make([]*anypb.Any, 0, len(vaiv1.SupportedVoyageAIModels()))
	for _, model := range vaiv1.SupportedVoyageAIModels() {
		cluster, err := buildVoyageRouterCluster(model, byModel[model].Backends)
		if err != nil {
			return "", err
		}
		resource, err := anypb.New(cluster)
		if err != nil {
			return "", fmt.Errorf("marshal model cluster %q: %w", model, err)
		}
		resources = append(resources, resource)
	}

	return marshalJSON(&discoveryv3.DiscoveryResponse{
		Resources: resources,
		TypeUrl:   "type.googleapis.com/envoy.config.cluster.v3.Cluster",
	})
}

func buildVoyageRouterCluster(model vaiv1.VoyageAIModel, backends []voyageRouterBackend) (*clusterv3.Cluster, error) {
	clusterName := voyageRouterClusterName(model)
	upstreamHTTP, err := anypb.New(&upstreamhttpv3.HttpProtocolOptions{
		UpstreamProtocolOptions: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_{
			ExplicitHttpConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig{
				ProtocolConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{
					HttpProtocolOptions: &corev3.Http1ProtocolOptions{},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal upstream HTTP options: %w", err)
	}

	endpoints := make([]*endpointv3.LbEndpoint, 0, len(backends))
	transportSocketMatches := make([]*clusterv3.Cluster_TransportSocketMatch, 0, len(backends)+1)
	for _, backend := range backends {
		if backend.Port < 1 || backend.Port > 65535 {
			return nil, fmt.Errorf("invalid backend port %d", backend.Port)
		}
		endpoint := &endpointv3.LbEndpoint{
			HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
				Address:  socketAddress(backend.ServiceFQDN, uint32(backend.Port)),
				Hostname: backend.ServiceFQDN,
			}},
		}
		if backend.TLS {
			match, err := voyageRouterTransportSocketMatch(backend.ServiceFQDN)
			if err != nil {
				return nil, err
			}
			endpoint.Metadata = &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{
				"envoy.transport_socket_match": match.Match,
			}}
			transportSocketMatches = append(transportSocketMatches, match)
		}
		endpoints = append(endpoints, endpoint)
	}

	return &clusterv3.Cluster{
		Name:                 clusterName,
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STRICT_DNS},
		LbPolicy:             clusterv3.Cluster_ROUND_ROBIN,
		TypedExtensionProtocolOptions: map[string]*anypb.Any{
			"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": upstreamHTTP,
		},
		LoadAssignment: &endpointv3.ClusterLoadAssignment{
			ClusterName: clusterName,
			Endpoints:   []*endpointv3.LocalityLbEndpoints{{LbEndpoints: endpoints}},
		},
		TransportSocketMatches: transportSocketMatches,
	}, nil
}

func voyageRouterTransportSocketMatch(serviceFQDN string) (*clusterv3.Cluster_TransportSocketMatch, error) {
	match, err := structpb.NewStruct(map[string]any{"tls_server_name": serviceFQDN})
	if err != nil {
		return nil, fmt.Errorf("create TLS transport match for %q: %w", serviceFQDN, err)
	}
	transportSocket, err := buildVoyageRouterUpstreamTLSSocket(serviceFQDN)
	if err != nil {
		return nil, err
	}
	return &clusterv3.Cluster_TransportSocketMatch{
		Name:            "tls-" + serviceFQDN,
		Match:           match,
		TransportSocket: transportSocket,
	}, nil
}

func watchedFileDataSource(filename, directory string) *corev3.DataSource {
	return &corev3.DataSource{
		Specifier:        &corev3.DataSource_Filename{Filename: filename},
		WatchedDirectory: &corev3.WatchedDirectory{Path: directory},
	}
}

func buildVoyageRouterUpstreamTLSSocket(serviceFQDN string) (*corev3.TransportSocket, error) {
	tlsContext := &tlsv3.UpstreamTlsContext{
		Sni: serviceFQDN,
		CommonTlsContext: &tlsv3.CommonTlsContext{
			ValidationContextType: &tlsv3.CommonTlsContext_ValidationContext{
				ValidationContext: &tlsv3.CertificateValidationContext{
					TrustedCa: watchedFileDataSource(voyageRouterUpstreamTLSPath+"/"+voyageRouterUpstreamCAFile, voyageRouterUpstreamTLSPath),
					MatchTypedSubjectAltNames: []*tlsv3.SubjectAltNameMatcher{{
						SanType: tlsv3.SubjectAltNameMatcher_DNS,
						Matcher: &matcherv3.StringMatcher{MatchPattern: &matcherv3.StringMatcher_Exact{Exact: serviceFQDN}},
					}},
				},
			},
		},
	}
	typedConfig, err := anypb.New(tlsContext)
	if err != nil {
		return nil, fmt.Errorf("marshal upstream TLS context: %w", err)
	}
	return &corev3.TransportSocket{
		Name:       wellknown.TransportSocketTLS,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: typedConfig},
	}, nil
}

func buildVoyageRouterLDSJSON(models []voyageRouterModel, requestBodyLimitBytes int64, inboundTLS bool) (string, error) {
	listener, err := buildVoyageRouterListener(models, requestBodyLimitBytes, inboundTLS)
	if err != nil {
		return "", err
	}
	resource, err := anypb.New(listener)
	if err != nil {
		return "", fmt.Errorf("marshal Router listener: %w", err)
	}
	return marshalJSON(&discoveryv3.DiscoveryResponse{
		Resources: []*anypb.Any{resource},
		TypeUrl:   "type.googleapis.com/envoy.config.listener.v3.Listener",
	})
}

func buildVoyageRouterListener(models []voyageRouterModel, requestBodyLimitBytes int64, inboundTLS bool) (*listenerv3.Listener, error) {
	filters, err := buildVoyageRouterHTTPFilters(models, requestBodyLimitBytes)
	if err != nil {
		return nil, err
	}
	filterChain := &listenerv3.FilterChain{Filters: []*listenerv3.Filter{{
		Name:       wellknown.HTTPConnectionManager,
		ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: filters},
	}}}
	if inboundTLS {
		transportSocket, err := buildVoyageRouterDownstreamTLSSocket()
		if err != nil {
			return nil, err
		}
		filterChain.TransportSocket = transportSocket
	}
	return &listenerv3.Listener{
		Name:         "voyage_router_listener",
		Address:      socketAddress("0.0.0.0", voyageRouterListenerPort),
		FilterChains: []*listenerv3.FilterChain{filterChain},
	}, nil
}

func buildVoyageRouterHTTPFilters(models []voyageRouterModel, requestBodyLimitBytes int64) (*anypb.Any, error) {
	if requestBodyLimitBytes < 1 || requestBodyLimitBytes > int64(^uint32(0)) {
		return nil, fmt.Errorf("request body limit %d is outside Envoy's supported range", requestBodyLimitBytes)
	}
	bufferConfig, err := anypb.New(&bufferv3.Buffer{MaxRequestBytes: wrapperspb.UInt32(uint32(requestBodyLimitBytes))})
	if err != nil {
		return nil, fmt.Errorf("marshal buffer filter: %w", err)
	}
	jsonToMetadataConfig, err := anypb.New(&jsonmetadatav3.JsonToMetadata{
		RequestRules: &jsonmetadatav3.JsonToMetadata_MatchRules{
			AllowContentTypesRegex: &matcherv3.RegexMatcher{Regex: `(?i)^application/json(\s*;.*)?$`},
			Rules: []*jsonmetadatav3.JsonToMetadata_Rule{{
				Selectors: []*jsonmetadatav3.JsonToMetadata_Selector{{
					Selector: &jsonmetadatav3.JsonToMetadata_Selector_Key{Key: "model"},
				}},
				OnPresent: &jsonmetadatav3.JsonToMetadata_KeyValuePair{
					MetadataNamespace: voyageRouterMetadataNamespace,
					Key:               "model",
				},
			}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal JSON-to-metadata filter: %w", err)
	}
	luaSource, err := buildVoyageRouterLua(models)
	if err != nil {
		return nil, fmt.Errorf("render VoyageRouter Lua filter: %w", err)
	}
	luaConfig, err := anypb.New(&luav3.Lua{DefaultSourceCode: &corev3.DataSource{
		Specifier: &corev3.DataSource_InlineString{InlineString: luaSource},
	}})
	if err != nil {
		return nil, fmt.Errorf("marshal Lua filter: %w", err)
	}
	routerConfig, err := anypb.New(&routerv3.Router{})
	if err != nil {
		return nil, fmt.Errorf("marshal router filter: %w", err)
	}
	accessLog, err := buildHCMAccessLog()
	if err != nil {
		return nil, fmt.Errorf("build Router access log: %w", err)
	}

	hcm := &hcmv3.HttpConnectionManager{
		StatPrefix: "voyage_router",
		CodecType:  hcmv3.HttpConnectionManager_AUTO,
		RouteSpecifier: &hcmv3.HttpConnectionManager_RouteConfig{RouteConfig: &routev3.RouteConfiguration{
			Name:             "voyage_router_routes",
			VirtualHosts:     []*routev3.VirtualHost{buildVoyageRouterVirtualHost()},
			ValidateClusters: wrapperspb.Bool(false),
		}},
		HttpFilters: []*hcmv3.HttpFilter{
			{Name: wellknown.Buffer, ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: bufferConfig}},
			{Name: "envoy.filters.http.json_to_metadata", ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: jsonToMetadataConfig}},
			{Name: wellknown.Lua, ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: luaConfig}},
			{Name: wellknown.Router, ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: routerConfig}},
		},
		Http2ProtocolOptions: &corev3.Http2ProtocolOptions{},
		LocalReplyConfig:     voyageRouterLocalReplyConfig(),
		AccessLog:            accessLog,
	}
	return anypb.New(hcm)
}

func buildVoyageRouterVirtualHost() *routev3.VirtualHost {
	routes := make([]*routev3.Route, 0, len(vaiv1.SupportedVoyageAIModels()))
	for _, model := range vaiv1.SupportedVoyageAIModels() {
		cluster := voyageRouterClusterName(model)
		routes = append(routes, &routev3.Route{
			Match: &routev3.RouteMatch{
				PathSpecifier: &routev3.RouteMatch_Prefix{Prefix: "/"},
				Headers: []*routev3.HeaderMatcher{{
					Name:                 voyageRouterInternalHeader,
					HeaderMatchSpecifier: &routev3.HeaderMatcher_ExactMatch{ExactMatch: cluster},
				}},
			},
			Action: &routev3.Route_Route{Route: &routev3.RouteAction{
				ClusterSpecifier: &routev3.RouteAction_Cluster{Cluster: cluster},
				HostRewriteSpecifier: &routev3.RouteAction_AutoHostRewrite{
					AutoHostRewrite: wrapperspb.Bool(true),
				},
				AppendXForwardedHost: true,
				Timeout:              durationpb.New(0),
			}},
			RequestHeadersToRemove: []string{voyageRouterInternalHeader},
		})
	}
	return &routev3.VirtualHost{Name: "voyage_router", Domains: []string{"*"}, Routes: routes}
}

func voyageRouterLocalReplyConfig() *hcmv3.LocalReplyConfig {
	jsonFormat, err := structpb.NewStruct(map[string]any{
		"error": map[string]any{
			"message": "%LOCAL_REPLY_BODY%",
			"type":    "invalid_request_error",
		},
	})
	if err != nil {
		panic(err)
	}
	return &hcmv3.LocalReplyConfig{BodyFormat: &corev3.SubstitutionFormatString{
		Format: &corev3.SubstitutionFormatString_JsonFormat{JsonFormat: jsonFormat},
	}}
}

// voyageRouterAPIPaths are the VoyageAI inference endpoints the Router forwards.
var voyageRouterAPIPaths = []string{
	voyageAIEmbeddingsPath,
	voyageAIContextualizedEmbeddingsPath,
	voyageAIMultimodalEmbeddingsPath,
	voyageAIRerankPath,
}

func buildVoyageRouterLua(models []voyageRouterModel) (string, error) {
	modelAvailability := make(map[string]string, len(models))
	for _, model := range models {
		if !model.Selected {
			continue
		}
		if len(model.Backends) == 0 {
			modelAvailability[string(model.Model)] = ""
			continue
		}
		modelAvailability[string(model.Model)] = voyageRouterClusterName(model.Model)
	}
	keys := make([]string, 0, len(modelAvailability))
	for key := range modelAvailability {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	templateModels := make([]voyageRouterLuaTemplateModel, 0, len(keys))
	for _, key := range keys {
		templateModels = append(templateModels, voyageRouterLuaTemplateModel{Name: key, Cluster: modelAvailability[key]})
	}

	var output bytes.Buffer
	if err := voyageRouterLuaTemplate.Execute(&output, voyageRouterLuaTemplateData{
		Models:            templateModels,
		Paths:             voyageRouterAPIPaths,
		InternalHeader:    voyageRouterInternalHeader,
		MetadataNamespace: voyageRouterMetadataNamespace,
	}); err != nil {
		return "", err
	}
	return output.String(), nil
}

func buildVoyageRouterDownstreamTLSSocket() (*corev3.TransportSocket, error) {
	tlsContext := &tlsv3.DownstreamTlsContext{CommonTlsContext: &tlsv3.CommonTlsContext{
		TlsCertificates: []*tlsv3.TlsCertificate{{
			CertificateChain: watchedFileDataSource(voyageRouterInboundTLSPath+"/tls.crt", voyageRouterInboundTLSPath),
			PrivateKey:       watchedFileDataSource(voyageRouterInboundTLSPath+"/tls.key", voyageRouterInboundTLSPath),
		}},
		AlpnProtocols: []string{"h2", "http/1.1"},
	}}
	typedConfig, err := anypb.New(tlsContext)
	if err != nil {
		return nil, fmt.Errorf("marshal inbound TLS context: %w", err)
	}
	return &corev3.TransportSocket{
		Name:       wellknown.TransportSocketTLS,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: typedConfig},
	}, nil
}
