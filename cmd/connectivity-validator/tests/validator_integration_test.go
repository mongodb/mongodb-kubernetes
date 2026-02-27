package validator_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/migration/connectivitycheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	mongoImage  = "mongodb/mongodb-community-server:7.0-ubi8"
	keyfileBody = "localtestkey123"
)

// startMongodWithKeyfile starts a standalone mongod with keyfile auth and returns
// the host:port reachable from the test process.
func startMongodWithKeyfile(ctx context.Context, t *testing.T) string {
	t.Helper()

	// Write the keyfile inline so it is owned by the mongod process user.
	// testcontainers.ContainerFile copies as root, which mongod rejects.
	startCmd := fmt.Sprintf(
		`printf '%s' > /tmp/mongo-keyfile && chmod 400 /tmp/mongo-keyfile && exec mongod --auth --keyFile /tmp/mongo-keyfile --bind_ip_all`,
		keyfileBody,
	)

	req := testcontainers.ContainerRequest{
		Image:        mongoImage,
		Cmd:          []string{"/bin/bash", "-c", startCmd},
		ExposedPorts: []string{"27017/tcp"},
		WaitingFor:   wait.ForLog("Waiting for connections").WithStartupTimeout(60 * time.Second),
	}

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
		assert.Equal(t, connectivitycheck.ExitMemberUnreachable, connectivitycheck.Validate(ctx, cfg))
	})

	t.Run("DNSFailed_ExternalMember", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: connStr,
			ExternalMembers:  []string{"nonexistent.invalid:27017"},
			AuthMechanism:    "SCRAM-SHA-256",
			KeyfilePath:      tempKeyfile(t, keyfileBody),
		}
		assert.Equal(t, connectivitycheck.ExitDNSFailed, connectivitycheck.Validate(ctx, cfg))
	})
}

// TestValidate_TwoMembers_BothReachable is kept separate as it needs two containers.
// Both are started in parallel to halve startup time.
func TestValidate_TwoMembers(t *testing.T) {
	ctx := context.Background()

	addr1Ch := make(chan string, 1)
	addr2Ch := make(chan string, 1)
	go func() { addr1Ch <- startMongodWithKeyfile(ctx, t) }()
	go func() { addr2Ch <- startMongodWithKeyfile(ctx, t) }()
	addr1, addr2 := <-addr1Ch, <-addr2Ch

	t.Run("TwoMembers_BothReachable", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: fmt.Sprintf("mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr1),
			ExternalMembers:  []string{addr1, addr2},
			AuthMechanism:    "SCRAM-SHA-256",
			KeyfilePath:      tempKeyfile(t, keyfileBody),
		}
		assert.Equal(t, connectivitycheck.ExitSuccess, connectivitycheck.Validate(ctx, cfg))
	})
	t.Run("TwoMembers_OneUnReachable", func(t *testing.T) {
		cfg := connectivitycheck.Config{
			ConnectionString: fmt.Sprintf("mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr1),
			ExternalMembers:  []string{addr1, "localhost:1111"},
			AuthMechanism:    "SCRAM-SHA-256",
			KeyfilePath:      tempKeyfile(t, keyfileBody),
		}
		assert.Equal(t, connectivitycheck.ExitMemberUnreachable, connectivitycheck.Validate(ctx, cfg))
	})
}
