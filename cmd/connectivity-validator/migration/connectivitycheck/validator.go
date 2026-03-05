package connectivitycheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	driver "go.mongodb.org/mongo-driver/x/mongo/driver"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"
	"go.uber.org/zap"
)

// Exit codes returned by Validate. Mirrored in pkg/migration/exitcodes.go for operator use.
const (
	ExitSuccess           = 0
	ExitUnknown           = 1
	ExitAuthFailed        = 2
	ExitRoleNotFound      = 3
	ExitMemberUnreachable = 4
	ExitDNSFailed         = 5
	ExitTLSFailed         = 6
)

// Config holds all inputs the validator needs; populated from env vars in main.go.
type Config struct {
	// ConnectionString is the replica set connection string for the initial auth check.
	ConnectionString string
	// ExternalMembers is the list of host:port pairs to ping directly.
	ExternalMembers []string
	// AuthMechanism is either "SCRAM-SHA-256" or "MONGODB-X509".
	AuthMechanism string
	// KeyfilePath is the path to the keyfile secret mount (SCRAM).
	KeyfilePath string
	// CertPath is the path to the combined cert+key PEM (X509).
	CertPath string
	// CAPath is the path to the CA PEM (X509).
	CAPath string
	// SubjectDN is the X.509 subject DN used as the username.
	SubjectDN string
}

// ExitCodeName returns a short name for the exit code for logging.
func ExitCodeName(code int) string {
	switch code {
	case ExitSuccess:
		return "Success"
	case ExitUnknown:
		return "Unknown"
	case ExitAuthFailed:
		return "AuthFailed"
	case ExitRoleNotFound:
		return "RoleNotFound"
	case ExitMemberUnreachable:
		return "MemberUnreachable"
	case ExitDNSFailed:
		return "DNSFailed"
	case ExitTLSFailed:
		return "TLSFailed"
	default:
		return fmt.Sprintf("Exit%d", code)
	}
}

// Validate runs the full connectivity check and returns an exit code.
func Validate(ctx context.Context, cfg Config) int {
	log := zap.S()
	log.Infow("Connectivity validation started",
		"authMechanism", cfg.AuthMechanism,
		"connectionString", cfg.ConnectionString,
		"externalMembersCount", len(cfg.ExternalMembers),
		"externalMembers", cfg.ExternalMembers,
		"keyfilePath", cfg.KeyfilePath,
		"certPath", cfg.CertPath,
		"caPath", cfg.CAPath,
		"subjectDN", cfg.SubjectDN,
	)

	clientOpts, err := buildClientOptions(cfg, cfg.ConnectionString)
	if err != nil {
		code := classifyError(err)
		log.Warnw("Failed to build client options", "error", err, "exitCode", code, "exitCodeName", ExitCodeName(code))
		return code
	}
	log.Debugw("Client options built successfully")

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		code := classifyError(err)
		log.Warnw("Failed to connect to MongoDB", "error", err, "exitCode", code, "exitCodeName", ExitCodeName(code))
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
		log.Warnw("MongoDB ping failed", "error", err, "exitCode", code, "exitCodeName", ExitCodeName(code))
		return code
	}
	log.Debugw("MongoDB ping succeeded")

	if !hasSystemRole(ctx, client) {
		log.Warnw("__system@local role not found", "exitCode", ExitRoleNotFound, "exitCodeName", ExitCodeName(ExitRoleNotFound))
		return ExitRoleNotFound
	}
	log.Debugw("__system@local role verified")

	for i, member := range cfg.ExternalMembers {
		log.Infow("Pinging external member", "member", member, "index", i+1, "total", len(cfg.ExternalMembers))
		if code := pingMemberDirect(ctx, member, cfg); code != ExitSuccess {
			log.Warnw("External member ping failed", "member", member, "exitCode", code, "exitCodeName", ExitCodeName(code))
			return code
		}
		log.Debugw("External member reachable", "member", member)
	}

	log.Infow("Connectivity validation passed", "exitCode", ExitSuccess)
	return ExitSuccess
}

func buildClientOptions(cfg Config, uri string) (*options.ClientOptions, error) {
	log := zap.S()
	opts := options.Client().ApplyURI(uri)

	switch cfg.AuthMechanism {
	case "SCRAM-SHA-256":
		log.Debugw("Using SCRAM-SHA-256 auth", "keyfilePath", cfg.KeyfilePath)
		keyfile, err := os.ReadFile(cfg.KeyfilePath)
		if err != nil {
			log.Warnw("Failed to read keyfile", "keyfilePath", cfg.KeyfilePath, "error", err)
			return nil, fmt.Errorf("reading keyfile: %w", err)
		}
		password := strings.TrimSpace(string(keyfile))
		if len(password) == 0 {
			log.Warnw("Keyfile is empty", "keyfilePath", cfg.KeyfilePath)
		}
		opts.SetAuth(options.Credential{
			AuthMechanism: "SCRAM-SHA-256",
			AuthSource:    "local",
			Username:      "__system",
			Password:      password,
		})
	case "MONGODB-X509":
		log.Debugw("Using MONGODB-X509 auth", "certPath", cfg.CertPath, "caPath", cfg.CAPath, "subjectDN", cfg.SubjectDN)
		tlsCfg, err := buildTLSConfig(cfg.CertPath, cfg.CAPath)
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
		opts.SetAuth(options.Credential{
			AuthMechanism: "MONGODB-X509",
			Username:      cfg.SubjectDN,
		})
	default:
		log.Debugw("No auth mechanism", "authMechanism", cfg.AuthMechanism)
	}
	return opts, nil
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

func hasSystemRole(ctx context.Context, client *mongo.Client) bool {
	log := zap.S()
	var result bson.M
	err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&result)
	if err != nil {
		log.Warnw("connectionStatus command failed", "error", err)
		return false
	}
	authInfo, ok := result["authInfo"].(bson.M)
	if !ok {
		log.Warnw("connectionStatus missing or invalid authInfo", "resultKeys", keys(result))
		return false
	}
	// MongoDB does not populate authenticatedUserRoles for internal __system auth —
	// check authenticatedUsers instead.
	if users, ok := authInfo["authenticatedUsers"].(bson.A); ok {
		for _, u := range users {
			entry, ok := u.(bson.M)
			if !ok {
				continue
			}
			if entry["user"] == "__system" && entry["db"] == "local" {
				log.Debugw("Found __system@local in authenticatedUsers")
				return true
			}
		}
	}
	// Fallback: some MongoDB versions do populate authenticatedUserRoles.
	if roles, ok := authInfo["authenticatedUserRoles"].(bson.A); ok {
		for _, r := range roles {
			entry, ok := r.(bson.M)
			if !ok {
				continue
			}
			if entry["role"] == "__system" && entry["db"] == "local" {
				log.Debugw("Found __system@local in authenticatedUserRoles")
				return true
			}
		}
	}
	log.Warnw("__system@local not found in authInfo", "authInfoKeys", keys(authInfo))
	return false
}

// keys returns the keys of a bson.M for logging (avoid logging full auth payload).
func keys(m bson.M) []string {
	if m == nil {
		return nil
	}
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	return k
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
	return ExitSuccess
}

func classifyError(err error) int {
	if err == nil {
		return ExitSuccess
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
				if code != ExitUnknown {
					return code
				}
			}
		}
		return ExitMemberUnreachable
	}
	code := classifyConnectionError(err)
	log.Debugw("Classified error", "error", err, "errorType", fmt.Sprintf("%T", err), "exitCode", code)
	return code
}

// classifyConnectionError classifies a ConnectionError or its inner cause.
func classifyConnectionError(err error) int {
	if err == nil {
		return ExitSuccess
	}
	log := zap.S()
	// ConnectionError.Wrapped holds the actual cause (net.OpError, x509 error, etc.).
	var connErr topology.ConnectionError
	if errors.As(err, &connErr) {
		log.Debugw("Unwrapping ConnectionError", "wrapped", connErr.Wrapped)
		return classifyConnectionError(connErr.Wrapped)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		log.Debugw("Classified as DNS error", "err", dnsErr)
		return ExitDNSFailed
	}
	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		log.Debugw("Classified as cert invalid", "reason", certInvalidErr.Reason)
		return ExitTLSFailed
	}
	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		log.Debugw("Classified as unknown authority")
		return ExitTLSFailed
	}
	// Auth failures during connection handshake surface as driver.Error (not mongo.CommandError).
	var drvErr driver.Error
	if errors.As(err, &drvErr) && drvErr.Code == 18 {
		log.Debugw("Classified as auth failed (driver)", "code", drvErr.Code)
		return ExitAuthFailed
	}
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) && cmdErr.Code == 18 {
		log.Debugw("Classified as auth failed (command)", "code", cmdErr.Code)
		return ExitAuthFailed
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		log.Debugw("Classified as network error", "timeout", netErr.Timeout(), "temporary", netErr.Temporary())
		return ExitMemberUnreachable
	}
	log.Debugw("Unclassified error", "error", err, "type", fmt.Sprintf("%T", err))
	return ExitUnknown
}
