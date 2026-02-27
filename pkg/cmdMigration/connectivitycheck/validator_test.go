//go:build integration

package connectivitycheck

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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
// the host:port reachable from the test process. No replica set is needed —
// the validator just needs a mongod that requires __system auth.
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

func TestValidate_SCRAM_Success(t *testing.T) {
	ctx := context.Background()
	addr := startMongodWithKeyfile(ctx, t)

	cfg := Config{
		ConnectionString: fmt.Sprintf("mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr),
		ExternalMembers:  []string{addr},
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      tempKeyfile(t, keyfileBody),
	}
	assert.Equal(t, ExitSuccess, Validate(ctx, cfg))
}

func TestValidate_SCRAM_WrongKeyfile(t *testing.T) {
	ctx := context.Background()
	addr := startMongodWithKeyfile(ctx, t)

	cfg := Config{
		ConnectionString: fmt.Sprintf("mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr),
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      tempKeyfile(t, "wrongkey"),
	}
	assert.Equal(t, ExitAuthFailed, Validate(ctx, cfg))
}

func TestValidate_DNSFailed_ConnectionString(t *testing.T) {
	cfg := Config{
		ConnectionString: "mongodb://nonexistent.invalid:27017/?serverSelectionTimeoutMS=3000",
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      tempKeyfile(t, keyfileBody),
	}
	assert.Equal(t, ExitDNSFailed, Validate(context.Background(), cfg))
}

func TestValidate_MemberUnreachable(t *testing.T) {
	cfg := Config{
		ConnectionString: "mongodb://localhost:27999/?serverSelectionTimeoutMS=3000",
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      tempKeyfile(t, keyfileBody),
	}
	assert.Equal(t, ExitMemberUnreachable, Validate(context.Background(), cfg))
}

func TestValidate_DNSFailed_ExternalMember(t *testing.T) {
	ctx := context.Background()
	addr := startMongodWithKeyfile(ctx, t)

	cfg := Config{
		ConnectionString: fmt.Sprintf("mongodb://%s/?directConnection=true&serverSelectionTimeoutMS=5000", addr),
		ExternalMembers:  []string{"nonexistent.invalid:27017"},
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      tempKeyfile(t, keyfileBody),
	}
	assert.Equal(t, ExitDNSFailed, Validate(ctx, cfg))
}
