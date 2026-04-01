package validator_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/migration/connectivitycheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	mongoImage  = "mongodb/mongodb-community-server:8.0-ubi8"
	keyfileBody = "localtestkey123"
)

// keyfileSetupCmd writes keyfileBody to the mongod keyfile path inside the container.
// It is embedded in every container start command that needs keyfile auth.
// Built as a var (not a const) so it references keyfileBody rather than repeating the literal.
var keyfileSetupCmd = fmt.Sprintf(`printf '%s' > /tmp/mongo-keyfile && chmod 400 /tmp/mongo-keyfile`, keyfileBody)

// startMongodWithKeyfile starts a standalone mongod with keyfile auth and returns
// the host:port reachable from the test process.
func startMongodWithKeyfile(ctx context.Context, t *testing.T) string {
	t.Helper()

	// Write the keyfile inline so it is owned by the mongod process user.
	// testcontainers.ContainerFile copies as root, which mongod rejects.
	startCmd := keyfileSetupCmd + ` && exec mongod --auth --keyFile /tmp/mongo-keyfile --bind_ip_all`

	req := testcontainers.ContainerRequest{
		Image:        mongoImage,
		Cmd:          []string{"/bin/bash", "-c", startCmd},
		ExposedPorts: []string{"27017/tcp"},
		WaitingFor:   wait.ForLog("Waiting for connections").WithStartupTimeout(60 * time.Second),
	}
	return startContainer(ctx, t, req)
}

// startMongodWithTLS starts a standalone mongod with TLS required and keyfile auth.
// Certificates are generated in Go (no openssl dependency) and bind-mounted into the
// container. It returns the host:port address and the path to the CA cert on the host
// so callers can supply tlsCAFile in the connection string.
func startMongodWithTLS(ctx context.Context, t *testing.T) (addr, caPath string) {
	t.Helper()

	// Generate certs on the host using Go crypto so we don't depend on openssl being
	// present in the image. generateClusterCertBundle produces ca.crt + srv.pem which
	// is all mongod needs for TLS-only mode.
	certsDir := generateClusterCertBundle(t)

	startCmd := keyfileSetupCmd + ` && exec mongod --tlsMode requireTLS --tlsCertificateKeyFile /certs/srv.pem --tlsCAFile /certs/ca.crt --tlsAllowConnectionsWithoutCertificates --auth --keyFile /tmp/mongo-keyfile --bind_ip_all`
	req := testcontainers.ContainerRequest{
		Image:        mongoImage,
		Cmd:          []string{"/bin/bash", "-c", startCmd},
		ExposedPorts: []string{"27017/tcp"},
		WaitingFor:   wait.ForLog("Waiting for connections").WithStartupTimeout(60 * time.Second),
		Mounts:       testcontainers.Mounts(testcontainers.BindMount(certsDir, "/certs")),
	}
	return startContainer(ctx, t, req), filepath.Join(certsDir, "ca.crt")
}

// startContainer starts a testcontainer with the given request, registers a cleanup
// hook to terminate it, and returns the mapped host:port for port 27017.
func startContainer(ctx context.Context, t *testing.T, req testcontainers.ContainerRequest) string {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "27017")
	require.NoError(t, err)
	return fmt.Sprintf("%s:%s", host, port.Port())
}

// writePEMFile encodes certDER and keyDER as PEM blocks and writes them to path.
// Used by generateClusterCertBundle for every cert+key pair it produces.
func writePEMFile(t *testing.T, path string, certDER, keyDER []byte) {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))
}

// tempKeyfile writes content to a temp file and returns its path.
func tempKeyfile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "keyfile")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// TestValidate_SingleMongod groups all tests that need exactly one running mongod,
// sharing the container across subtests to avoid redundant startup overhead.
func TestValidate_SingleMongod(t *testing.T) {
	ctx := context.Background()
	addr := startMongodWithKeyfile(ctx, t)
	connStr := fmt.Sprintf("mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr)

	t.Run("SCRAM_Success", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: connStr,
			ExternalMembers:  []string{addr},
			AuthMechanism:    "SCRAM-SHA-256",
			KeyfilePath:      tempKeyfile(t, keyfileBody),
		}
		assert.Equal(t, connectivitycheck.ExitSuccess, connectivitycheck.Validate(ctx, cfg))
	})

	t.Run("WrongKeyfile", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: connStr,
			AuthMechanism:    "SCRAM-SHA-256",
			KeyfilePath:      tempKeyfile(t, "wrongkey"),
		}
		assert.Equal(t, connectivitycheck.ExitAuthFailed, connectivitycheck.Validate(ctx, cfg))
	})

	t.Run("OneUnreachable", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: connStr,
			ExternalMembers:  []string{addr, "localhost:27999"},
			AuthMechanism:    "SCRAM-SHA-256",
			KeyfilePath:      tempKeyfile(t, keyfileBody),
		}
		assert.Equal(t, connectivitycheck.ExitNetworkFailed, connectivitycheck.Validate(ctx, cfg))
	})

	t.Run("DNSFailed_ExternalMember", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: connStr,
			ExternalMembers:  []string{"nonexistent.invalid:27017"},
			AuthMechanism:    "SCRAM-SHA-256",
			KeyfilePath:      tempKeyfile(t, keyfileBody),
		}
		assert.Equal(t, connectivitycheck.ExitNetworkFailed, connectivitycheck.Validate(ctx, cfg))
	})
}

// TestValidate_TLS tests that SCRAM-SHA-256 authentication succeeds over a TLS-encrypted connection.
func TestValidate_TLS(t *testing.T) {
	ctx := context.Background()
	addr, caPath := startMongodWithTLS(ctx, t)

	t.Run("TLSSuccess", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: fmt.Sprintf(
				"mongodb://%s/?directConnection=true&tls=true&tlsCAFile=%s&serverSelectionTimeoutMS=5000",
				addr, caPath,
			),
			AuthMechanism: "SCRAM-SHA-256",
			KeyfilePath:   tempKeyfile(t, keyfileBody),
		}
		assert.Equal(t, connectivitycheck.ExitSuccess, connectivitycheck.Validate(ctx, cfg))
	})
}

// TestValidate_X509Auth tests true X.509 client authentication using a shared CA that signs
// both the server and client certificates.
//
// Certificates are generated in Go (no openssl) and bind-mounted into the container so
// mongod can read them at startup. The server is started with --clusterAuthMode x509: when a
// client presents a cert that shares the same O and OU as the server cert, MongoDB recognises
// it as a cluster member and authenticates it as the built-in __system user in the local
// database — no createUser step needed.
func TestValidate_X509Auth(t *testing.T) {
	ctx := context.Background()

	// generateClusterCertBundle is called first so its t.TempDir cleanup is registered
	// before the container cleanup; LIFO ordering then terminates the container before
	// removing the cert directory.
	certsDir := generateClusterCertBundle(t)

	req := testcontainers.ContainerRequest{
		Image: mongoImage,
		Cmd: []string{
			"mongod",
			"--tlsMode", "requireTLS",
			"--tlsCertificateKeyFile", "/certs/srv.pem",
			"--tlsCAFile", "/certs/ca.crt",
			"--clusterAuthMode", "x509",
			"--auth",
			"--bind_ip_all",
		},
		ExposedPorts: []string{"27017/tcp"},
		WaitingFor:   wait.ForLog("Waiting for connections").WithStartupTimeout(90 * time.Second),
		Mounts: testcontainers.Mounts(
			testcontainers.BindMount(certsDir, "/certs"),
		),
	}
	addr := startContainer(ctx, t, req)

	t.Run("X509_Success", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: fmt.Sprintf(
				"mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr,
			),
			AuthMechanism: "MONGODB-X509",
			CAPath:        filepath.Join(certsDir, "ca.crt"),
			CertPath:      filepath.Join(certsDir, "client.pem"),
		}
		assert.Equal(t, connectivitycheck.ExitSuccess, connectivitycheck.Validate(ctx, cfg))
	})
}

// generateClusterCertBundle creates a temp directory containing three PEM files:
//
//   - ca.crt     – self-signed CA certificate
//   - srv.pem    – CA-signed server cert+key (localhost SAN; O=TestOrg, OU=Server)
//   - client.pem – CA-signed client cert+key (same O/OU as server)
//
// The matching O and OU cause MongoDB's --clusterAuthMode x509 to treat the client
// as a cluster member, authenticating it as __system@local without any createUser step.
// All files are written at mode 0644 so the container's mongod user can read them.
func generateClusterCertBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// ── CA ──────────────────────────────────────────────────────────────────
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	var caBuf bytes.Buffer
	require.NoError(t, pem.Encode(&caBuf, &pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), caBuf.Bytes(), 0644))

	// ── Server cert (CA-signed, localhost SAN, matching O/OU) ────────────────
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	srvTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			Organization:       []string{"TestOrg"},
			OrganizationalUnit: []string{"Server"},
			CommonName:         "localhost",
		},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTemplate, caCert, &srvKey.PublicKey, caKey)
	require.NoError(t, err)
	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	require.NoError(t, err)
	writePEMFile(t, filepath.Join(dir, "srv.pem"), srvDER, srvKeyDER)

	// ── Client cert (CA-signed, same O/OU triggers cluster-auth match) ───────
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject: pkix.Name{
			Organization:       []string{"TestOrg"},
			OrganizationalUnit: []string{"Server"},
			CommonName:         "test-client",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	require.NoError(t, err)
	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	require.NoError(t, err)
	writePEMFile(t, filepath.Join(dir, "client.pem"), clientDER, clientKeyDER)

	return dir
}

// TestValidate_TwoMembers_BothReachable is kept separate as it needs two containers,
// verifying that the member-iteration loop returns ExitSuccess when all members are reachable.
// Both containers are started in parallel to halve startup time.
func TestValidate_TwoMembers_BothReachable(t *testing.T) {
	ctx := context.Background()

	addr1Ch := make(chan string, 1)
	addr2Ch := make(chan string, 1)
	go func() { addr1Ch <- startMongodWithKeyfile(ctx, t) }()
	go func() { addr2Ch <- startMongodWithKeyfile(ctx, t) }()
	addr1, addr2 := <-addr1Ch, <-addr2Ch

	cfg := connectivitycheck.Config{
		ConnectionString: fmt.Sprintf("mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr1),
		ExternalMembers:  []string{addr1, addr2},
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      tempKeyfile(t, keyfileBody),
	}
	assert.Equal(t, connectivitycheck.ExitSuccess, connectivitycheck.Validate(ctx, cfg))
}
