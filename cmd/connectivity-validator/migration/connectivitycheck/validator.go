package connectivitycheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"
	"go.uber.org/zap"

	driver "go.mongodb.org/mongo-driver/x/mongo/driver"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/exitcode"
)

// mongodbErrAuthenticationFailed is MongoDB server error code AuthenticationFailed.
// https://www.mongodb.com/docs/manual/reference/error-codes/
const mongodbErrAuthenticationFailed int32 = 18

// Config holds all inputs the validator needs; populated from env vars in main.go.
type Config struct {
	// ConnectionString is the replica set connection string for the initial auth check.
	ConnectionString string
	// ExternalMembers is the list of host:port pairs to ping directly.
	ExternalMembers []string
	// AuthMechanism is "MONGODB-X509" to use X.509 client certificate auth. SCRAM values
	// ("SCRAM-SHA-256", "SCRAM-SHA-1") may be passed by the job builder but are intentionally
	// ignored. Those deployments are checked for reachability only.
	AuthMechanism string
	// CertPath is the path to the combined cert+key PEM (X509).
	CertPath string
	// CAPath is the path to the CA PEM (X509).
	CAPath string
	// SubjectDN is the X.509 subject DN used as the username.
	SubjectDN string
	// MongodTLSCAPath is the path to the CA PEM used to verify mongod TLS certificates.
	// When set, TLS transport is enabled for all connections even when using SCRAM auth.
	MongodTLSCAPath string
	// ClientCertRequired indicates that the mongod requires a client certificate
	// (clientCertificateMode: REQUIRE). When true, a missing cert file is an error rather
	// than a silent fallback to CA-only TLS.
	ClientCertRequired bool
}

// Validate runs the full connectivity check and returns an exit code.
func Validate(ctx context.Context, cfg Config) int {
	log := zap.S()
	log.Debugw("Connectivity validation started",
		"authMechanism", cfg.AuthMechanism,
		"connectionString", cfg.ConnectionString,
		"externalMembersCount", len(cfg.ExternalMembers),
		"externalMembers", cfg.ExternalMembers,
		"certPath", cfg.CertPath,
		"caPath", cfg.CAPath,
		"subjectDN", cfg.SubjectDN,
		"mongodTLSCAPath", cfg.MongodTLSCAPath,
	)

	clientOpts, err := buildClientOptions(cfg, cfg.ConnectionString)
	if err != nil {
		code := classifyError(err)
		log.Warnw("Failed to build client options", "error", err, "exitCode", code, "exitCodeName", exitcode.Name(code))
		return code
	}
	log.Debugw("Client options built successfully")

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		code := classifyError(err)
		log.Warnw("Failed to connect to MongoDB", "error", err, "exitCode", code, "exitCodeName", exitcode.Name(code))
		return code
	}
	defer func(client *mongo.Client, ctx context.Context) {
		err := client.Disconnect(ctx)
		if err != nil {
			log.Errorf("Failed to disconnect from MongoDB: %v", err)
		}
	}(client, ctx)
	log.Debugw("Connected to MongoDB")

	if err := client.Ping(ctx, nil); err != nil {
		code := classifyError(err)
		log.Warnw("MongoDB ping failed", "error", err, "exitCode", code, "exitCodeName", exitcode.Name(code))
		return code
	}
	log.Debugw("MongoDB ping succeeded")

	for i, member := range cfg.ExternalMembers {
		log.Debugw("Pinging external member", "member", member, "index", i+1, "total", len(cfg.ExternalMembers))
		if code := pingMemberDirect(ctx, member, cfg); code != exitcode.ExitSuccess {
			log.Warnw("External member ping failed", "member", member, "exitCode", code, "exitCodeName", exitcode.Name(code))
			return code
		}
		log.Debugw("External member reachable", "member", member)
	}

	log.Debugw("Connectivity validation passed", "exitCode", exitcode.ExitSuccess)
	return exitcode.ExitSuccess
}

func buildClientOptions(cfg Config, uri string) (*options.ClientOptions, error) {
	log := zap.S()
	opts := options.Client().ApplyURI(uri)

	switch cfg.AuthMechanism {
	case "MONGODB-X509":
		log.Debugw("Using MONGODB-X509 auth", "certPath", cfg.CertPath, "caPath", cfg.CAPath, "subjectDN", cfg.SubjectDN)
		if _, statErr := os.Stat(cfg.CertPath); statErr != nil {
			return nil, fmt.Errorf("stat X.509 cert: %w", statErr)
		}
		tlsCfg, err := buildTLSConfig(cfg.CertPath, cfg.CAPath)
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
		opts.SetAuth(options.Credential{
			AuthMechanism: "MONGODB-X509",
			Username:      cfg.SubjectDN,
		})
		return opts, nil
	default:
		log.Debugw("No auth mechanism", "authMechanism", cfg.AuthMechanism)
	}

	// Apply TLS transport whenever the mongod requires it. Covers SCRAM and the no-auth case.
	// If the agent cert file is present (TLS client auth is configured), include it so the
	// validator can connect to mongod instances that require client certificates.
	if cfg.MongodTLSCAPath != "" {
		var tlsCfg *tls.Config
		var err error
		if cfg.CertPath != "" {
			if _, statErr := os.Stat(cfg.CertPath); statErr == nil {
				log.Debugw("Configuring TLS transport with client cert", "caPath", cfg.MongodTLSCAPath, "certPath", cfg.CertPath)
				tlsCfg, err = buildTLSConfig(cfg.CertPath, cfg.MongodTLSCAPath)
			} else if os.IsNotExist(statErr) && !cfg.ClientCertRequired {
				log.Debugw("Configuring TLS transport (CA only)", "caPath", cfg.MongodTLSCAPath)
				tlsCfg, err = buildTLSConfigFromCA(cfg.MongodTLSCAPath)
			} else if os.IsNotExist(statErr) && cfg.ClientCertRequired {
				return nil, fmt.Errorf("client certificate required but not found at %q", cfg.CertPath)
			} else {
				return nil, fmt.Errorf("stat client cert %q: %w", cfg.CertPath, statErr)
			}
		} else {
			log.Debugw("Configuring TLS transport (CA only)", "caPath", cfg.MongodTLSCAPath)
			tlsCfg, err = buildTLSConfigFromCA(cfg.MongodTLSCAPath)
		}
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
	}
	return opts, nil
}

func buildTLSConfigFromCA(caPath string) (*tls.Config, error) {
	log := zap.S()
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		log.Warnw("Failed to read mongod CA file", "caPath", caPath, "error", err)
		return nil, fmt.Errorf("reading mongod CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		log.Warnw("Failed to parse mongod CA certificate", "caPath", caPath)
		return nil, fmt.Errorf("parsing mongod CA certificate")
	}
	return &tls.Config{RootCAs: caPool, MinVersion: tls.VersionTLS13}, nil
}

func buildTLSConfig(certPath, caPath string) (*tls.Config, error) {
	log := zap.S()
	cert, err := tls.LoadX509KeyPair(certPath, certPath)
	if err != nil {
		log.Warnw("Failed to load client cert", "certPath", certPath, "error", err)
		return nil, fmt.Errorf("loading client cert: %w", err)
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		log.Warnw("Failed to read CA file", "caPath", caPath, "error", err)
		return nil, fmt.Errorf("reading CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		log.Warnw("Failed to parse CA certificate", "caPath", caPath)
		return nil, fmt.Errorf("parsing CA certificate")
	}
	log.Debugw("TLS config built", "certPath", certPath, "caPath", caPath)
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func pingMemberDirect(ctx context.Context, hostPort string, cfg Config) int {
	log := zap.S()
	directURI := "mongodb://" + hostPort + "/?directConnection=true&serverSelectionTimeoutMS=5000"
	log.Debugw("Pinging member directly", "hostPort", hostPort)

	opts, err := buildClientOptions(cfg, directURI)
	if err != nil {
		code := classifyError(err)
		log.Warnw("buildClientOptions failed for direct ping", "hostPort", hostPort, "error", err, "exitCode", code)
		return code
	}
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		code := classifyError(err)
		log.Warnw("Connect failed for direct ping", "hostPort", hostPort, "error", err, "exitCode", code)
		return code
	}
	defer func(client *mongo.Client, ctx context.Context) {
		err := client.Disconnect(ctx)
		if err != nil {
			log.Errorf("Failed to disconnect from MongoDB: %v", err)
		}
	}(client, ctx)
	if err := client.Ping(ctx, nil); err != nil {
		code := classifyError(err)
		log.Warnw("Ping failed for direct connection", "hostPort", hostPort, "error", err, "exitCode", code)
		return code
	}
	return exitcode.ExitSuccess
}

func classifyError(err error) int {
	if err == nil {
		return exitcode.ExitSuccess
	}
	log := zap.S()
	// ServerSelectionError.Wrapped is just "server selection timeout" — the actual
	// per-server cause lives in Desc.Servers[i].LastError.
	var selErr topology.ServerSelectionError
	if errors.As(err, &selErr) {
		log.Debugw("ServerSelectionError", "numServers", len(selErr.Desc.Servers))
		for i, srv := range selErr.Desc.Servers {
			if srv.LastError != nil {
				code := classifyConnectionError(srv.LastError)
				log.Debugw("Server last error", "serverIndex", i, "addr", srv.Addr.String(), "error", srv.LastError, "classifiedCode", code)
				if code != exitcode.ExitUnknown {
					return code
				}
			}
		}
		return exitcode.ExitNetworkFailed
	}
	code := classifyConnectionError(err)
	log.Debugw("Classified error", "error", err, "errorType", fmt.Sprintf("%T", err), "exitCode", code)
	return code
}

// classifyConnectionError maps the error tree to an exit code.
func classifyConnectionError(err error) int {
	if err == nil {
		return exitcode.ExitSuccess
	}
	log := zap.S()
	// DNS implements net.Error; check DNS before net.Error.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		log.Debugw("Classified as DNS error", "err", dnsErr)
		return exitcode.ExitNetworkFailed
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		log.Debugw("Classified as cert invalid", "reason", certInvalid.Reason)
		return exitcode.ExitNetworkFailed
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		log.Debugw("Classified as unknown authority")
		return exitcode.ExitNetworkFailed
	}
	var drvErr driver.Error
	if errors.As(err, &drvErr) && drvErr.Code == mongodbErrAuthenticationFailed {
		log.Debugw("Classified as auth failed (driver)", "code", drvErr.Code)
		return exitcode.ExitAuthFailed
	}
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) && cmdErr.Code == mongodbErrAuthenticationFailed {
		log.Debugw("Classified as auth failed (command)", "code", cmdErr.Code)
		return exitcode.ExitAuthFailed
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		log.Debugw("Classified as network error", "timeout", netErr.Timeout())
		return exitcode.ExitNetworkFailed
	}
	log.Debugw("Unclassified error", "error", err, "type", fmt.Sprintf("%T", err))
	return exitcode.ExitUnknown
}
