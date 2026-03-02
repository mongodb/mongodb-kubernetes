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

// Validate runs the full connectivity check and returns an exit code.
func Validate(ctx context.Context, cfg Config) int {
	clientOpts, err := buildClientOptions(cfg, cfg.ConnectionString)
	if err != nil {
		return classifyError(err)
	}
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return classifyError(err)
	}
	defer func(client *mongo.Client, ctx context.Context) {
		err := client.Disconnect(ctx)
		if err != nil {
			zap.S().Errorf("Failed to disconnect from MongoDB: %v", err)
		}
	}(client, ctx)

	if err := client.Ping(ctx, nil); err != nil {
		return classifyError(err)
	}
	if !hasSystemRole(ctx, client) {
		return ExitRoleNotFound
	}

	for _, member := range cfg.ExternalMembers {
		if code := pingMemberDirect(ctx, member, cfg); code != ExitSuccess {
			return code
		}
	}
	return ExitSuccess
}

func buildClientOptions(cfg Config, uri string) (*options.ClientOptions, error) {
	opts := options.Client().ApplyURI(uri)

	switch cfg.AuthMechanism {
	case "SCRAM-SHA-256":
		keyfile, err := os.ReadFile(cfg.KeyfilePath)
		if err != nil {
			return nil, fmt.Errorf("reading keyfile: %w", err)
		}
		password := strings.TrimSpace(string(keyfile))
		opts.SetAuth(options.Credential{
			AuthMechanism: "SCRAM-SHA-256",
			AuthSource:    "local",
			Username:      "__system",
			Password:      password,
		})
	case "MONGODB-X509":
		tlsCfg, err := buildTLSConfig(cfg.CertPath, cfg.CAPath)
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
		opts.SetAuth(options.Credential{
			AuthMechanism: "MONGODB-X509",
			Username:      cfg.SubjectDN,
		})
	}
	return opts, nil
}

func buildTLSConfig(certPath, caPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, certPath)
	if err != nil {
		return nil, fmt.Errorf("loading client cert: %w", err)
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parsing CA certificate")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
	}, nil
}

func hasSystemRole(ctx context.Context, client *mongo.Client) bool {
	var result bson.M
	err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&result)
	if err != nil {
		return false
	}
	authInfo, ok := result["authInfo"].(bson.M)
	if !ok {
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
				return true
			}
		}
	}
	return false
}

func pingMemberDirect(ctx context.Context, hostPort string, cfg Config) int {
	directURI := "mongodb://" + hostPort + "/?directConnection=true&serverSelectionTimeoutMS=5000"
	opts, err := buildClientOptions(cfg, directURI)
	if err != nil {
		return classifyError(err)
	}
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return classifyError(err)
	}
	defer func(client *mongo.Client, ctx context.Context) {
		err := client.Disconnect(ctx)
		if err != nil {
			zap.S().Errorf("Failed to disconnect from MongoDB: %v", err)
		}
	}(client, ctx)
	if err := client.Ping(ctx, nil); err != nil {
		return classifyError(err)
	}
	return ExitSuccess
}

func classifyError(err error) int {
	if err == nil {
		return ExitSuccess
	}
	// ServerSelectionError.Wrapped is just "server selection timeout" — the actual
	// per-server cause lives in Desc.Servers[i].LastError.
	var selErr topology.ServerSelectionError
	if errors.As(err, &selErr) {
		for _, srv := range selErr.Desc.Servers {
			if srv.LastError != nil {
				if code := classifyConnectionError(srv.LastError); code != ExitUnknown {
					return code
				}
			}
		}
		return ExitMemberUnreachable
	}
	return classifyConnectionError(err)
}

// classifyConnectionError classifies a ConnectionError or its inner cause.
func classifyConnectionError(err error) int {
	if err == nil {
		return ExitSuccess
	}
	// ConnectionError.Wrapped holds the actual cause (net.OpError, x509 error, etc.).
	var connErr topology.ConnectionError
	if errors.As(err, &connErr) {
		return classifyConnectionError(connErr.Wrapped)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ExitDNSFailed
	}
	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		return ExitTLSFailed
	}
	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return ExitTLSFailed
	}
	// Auth failures during connection handshake surface as driver.Error (not mongo.CommandError).
	var drvErr driver.Error
	if errors.As(err, &drvErr) && drvErr.Code == 18 {
		return ExitAuthFailed
	}
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) && cmdErr.Code == 18 {
		return ExitAuthFailed
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ExitMemberUnreachable
	}
	return ExitUnknown
}

