//go:build integration

package mongodbproxy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"go.uber.org/zap/zaptest"
)

// The shared fixture contains the Agent binary built by Evergreen patch 9338.
const defaultAgentIntegrationImage = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent@sha256:646e57f8dd5c8f9c42b6e724cfb9d93298cf3781cf9d45e844837a9c8dc7de86"

const integrationMongoDBImage = "quay.io/mongodb/mongodb-enterprise-server:8.0.4-ubi9@sha256:3fb56fd5ef277019ac9559a3d93690db9225eebd0e6995504606ffe942ef5032"

var agentIntegrationImage = flag.String("agent-image", defaultAgentIntegrationImage, "compatible Linux amd64 Agent image")

// TestAgentRecoversMongod runs the proxy in the Go test process so an IDE or
// Delve can break in the real request handler. Run in Linux with Docker access,
// Docker authentication to the dev ECR registry, and support for amd64 executables:
//
//	go test -tags=integration -v -count=1 -run '^TestAgentRecoversMongod$' ./pkg/mongodbproxy
//
// Override the default Agent image with -args -agent-image=<image>.
// Testcontainers stages the Agent and MongoDB executables from their images.
// The Agent runs alongside the proxy, which starts and owns mongod.
// On macOS, use Docker Desktop. From the repository root, authenticate Docker
// to dev ECR, pull the Agent image, and run the test with the current proxy code:
//
//	docker pull --platform linux/amd64 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent@sha256:646e57f8dd5c8f9c42b6e724cfb9d93298cf3781cf9d45e844837a9c8dc7de86
//	work=$(mktemp -d)
//	chmod 755 "$work"
//	GOOS=linux GOARCH=$(go env GOHOSTARCH) CGO_ENABLED=0 go test -tags=integration -c -o "$work/mongodbproxy.test" ./pkg/mongodbproxy &&
//	  docker run --rm --init --platform linux/amd64 --network none --group-add 0 \
//	    -v /var/run/docker.sock:/var/run/docker.sock -v "$work:/test:ro" \
//	    -e TESTCONTAINERS_RYUK_DISABLED=true --entrypoint /test/mongodbproxy.test \
//	    quay.io/mongodb/mongodb-enterprise-server:8.0.4-ubi9@sha256:3fb56fd5ef277019ac9559a3d93690db9225eebd0e6995504606ffe942ef5032 \
//	    -test.v -test.run '^TestAgentRecoversMongod$' -test.timeout=3m
//
// Test cleanup removes the fixture containers even with Ryuk disabled.
func TestAgentRecoversMongod(t *testing.T) {
	work := t.TempDir()
	agentPath := copyIntegrationAgent(t, *agentIntegrationImage, work)
	stageIntegrationMongod(t, work)
	socket := filepath.Join(work, "proxy.sock")
	listener, err := Listen(UnixScheme + socket)
	require.NoError(t, err)
	log := zaptest.NewLogger(t).Sugar()
	sup := NewSupervisor(work, onlyIn(work), testEnv, log)
	server := &http.Server{Handler: NewHandler(sup, log), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, server.Shutdown(ctx))
		assert.ErrorIs(t, <-served, http.ErrServerClosed)
		assert.NoError(t, sup.Terminate(ctx))
	})

	// Ask the kernel for a free loopback port. Run in an isolated container if
	// other processes could claim it between this close and MongoDB startup.
	portProbe, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := portProbe.Addr().(*net.TCPAddr).Port
	require.NoError(t, portProbe.Close())
	config := writeAgentIntegrationConfig(t, work, port)
	require.Equal(t, StateIdle, sup.Status().State)

	agentLog, err := os.Create(filepath.Join(work, "agent.log"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = agentLog.Close()
		if t.Failed() {
			output, _ := os.ReadFile(agentLog.Name())
			t.Logf("Agent log tail:\n%s", output[max(0, len(output)-8192):])
		}
	})
	agent := exec.CommandContext(t.Context(), agentPath, "-noDaemonize", "-operatorMode",
		"-overrideLocalHost=127.0.0.1", "-cluster="+config, "-mongodProxyAddress="+UnixScheme+socket, "-binariesFixedPath="+work)
	agent.Dir, agent.Stdout, agent.Stderr = work, agentLog, agentLog
	require.NoError(t, agent.Start())
	agentExited := make(chan struct{})
	var agentErr error
	go func() {
		agentErr = agent.Wait()
		close(agentExited)
	}()
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		<-agentExited
	})

	waitForMongod := func(previousID string) Status {
		// The Agent takes about 10 seconds to replan. Leave room for startup
		// under emulation and a short pause at a breakpoint.
		deadline := time.NewTimer(time.Minute)
		defer deadline.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			status := sup.Status()
			if status.State == StateRunning && status.ExecutionID != previousID {
				return status
			}
			events, _ := sup.eventSnapshot()
			for _, event := range events {
				if event.Action == "start" && event.ErrorClass == "invalid_request" {
					t.Fatalf("proxy rejected the Agent start request: %s", event.Returned)
				}
			}
			select {
			case <-agentExited:
				t.Fatalf("Agent exited while waiting for mongod: %v", agentErr)
			case <-deadline.C:
				t.Fatalf("timed out waiting for a new mongod execution: %+v", status)
			case <-tick.C:
			}
		}
	}

	first := waitForMongod("")
	require.Equal(t, RoleMain, first.Role)
	require.NotEmpty(t, first.ExecutionID)
	require.NotNil(t, first.PID)
	t.Logf("Agent started mongod: PID %d, execution %s", *first.PID, first.ExecutionID)

	client, err := mongo.Connect(t.Context(), options.Client().ApplyURI(fmt.Sprintf("mongodb://127.0.0.1:%d/?directConnection=true", port)).
		SetWriteConcern(writeconcern.Journaled()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	collection := client.Database("smoke").Collection("smoke")
	document := bson.D{{Key: "_id", Value: "before-crash"}}
	_, err = collection.InsertOne(t.Context(), document)
	require.NoError(t, err)

	require.Greater(t, *first.PID, 1)
	require.NoError(t, syscall.Kill(*first.PID, syscall.SIGKILL))
	second := waitForMongod(first.ExecutionID)
	require.Equal(t, RoleMain, second.Role)
	require.NotEmpty(t, second.ExecutionID)
	require.NotNil(t, second.PID)
	t.Logf("Agent recovered mongod: PID %d, execution %s", *second.PID, second.ExecutionID)

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	body, err := json.Marshal(StopRequest{ExecutionID: first.ExecutionID, Role: RoleMain, TimeoutSeconds: 10})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/v1/process/stop", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	// Allow the server to return a result even if it incorrectly attempts the 10-second stop.
	resp, err := (&http.Client{Transport: transport, Timeout: 15 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var rejected errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rejected))
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Equal(t, "execution_mismatch", rejected.Code)
	require.NoError(t, collection.FindOne(t.Context(), document).Err())
	require.Equal(t, second.ExecutionID, sup.Status().ExecutionID)
	t.Log("stale stop returned 409 execution_mismatch and the document survived")
}

func copyIntegrationAgent(t *testing.T, image, work string) string {
	t.Helper()
	// Docker can copy files from a created container without running its entrypoint.
	// https://golang.testcontainers.org/features/files_and_mounts/#copying-files-from-a-container
	container, err := testcontainers.Run(t.Context(), image, testcontainers.WithNoStart(), testcontainers.WithImagePlatform("linux/amd64"))
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err, "create source container for Agent image %s", image)
	const agentBinary = "/agent/mongodb-agent"
	source, err := container.CopyFileFromContainer(t.Context(), agentBinary)
	require.NoError(t, err, "copy %s from Agent image %s", agentBinary, image)
	defer func() { _ = source.Close() }()
	path := filepath.Join(work, "mongodb-agent")
	writeIntegrationExecutable(t, path, source)
	return path
}

func stageIntegrationMongod(t *testing.T, work string) {
	t.Helper()
	// Bundle the image's loader and linked libraries to avoid host ABI dependencies.
	// https://golang.testcontainers.org/features/wait/exit/
	const bundle = "/tmp/mongod-fixture.tar"
	const pack = `deps=$(ldd /usr/bin/mongod)
case "$deps" in *"not found"*) printf '%s\n' "$deps" >&2; exit 1;; esac
files=$(printf '%s\n' "$deps" | awk '$2 == "=>" && $3 ~ /^\// {print $3} $1 ~ /^\// {print $1}')
test -n "$files"
tar --dereference --hard-dereference --transform='s|.*/||' -cf ` + bundle + ` /usr/bin/mongod $files`
	container, err := testcontainers.Run(t.Context(), integrationMongoDBImage,
		testcontainers.WithImagePlatform("linux/amd64"), testcontainers.WithEntrypoint("/bin/sh", "-ec"),
		testcontainers.WithCmd(pack), testcontainers.WithWaitStrategy(wait.ForExit()))
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)
	state, err := container.State(t.Context())
	require.NoError(t, err)
	require.Zero(t, state.ExitCode, "pack MongoDB executable and libraries")
	source, err := container.CopyFileFromContainer(t.Context(), bundle)
	require.NoError(t, err)
	defer func() { _ = source.Close() }()

	dir := filepath.Join(work, "mongo-runtime")
	require.NoError(t, os.Mkdir(dir, 0o700))
	archive := tar.NewReader(source)
	for {
		file, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		require.Equal(t, byte(tar.TypeReg), file.Typeflag, "unsupported archive entry %q", file.Name)
		require.True(t, filepath.IsLocal(file.Name) && filepath.Base(file.Name) == file.Name, "unsafe archive path %q", file.Name)
		writeIntegrationExecutable(t, filepath.Join(dir, file.Name), archive)
	}

	// exec keeps the same PID so the proxy can signal and reap mongod.
	const launcher = `#!/bin/sh
runtime=${0%/*}/mongo-runtime
exec "$runtime/ld-linux-x86-64.so.2" --library-path "$runtime" "$runtime/mongod" "$@"
`
	writeIntegrationExecutable(t, filepath.Join(work, "mongod"), bytes.NewBufferString(launcher))
}

func writeIntegrationExecutable(t *testing.T, path string, source io.Reader) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	n, err := io.Copy(file, source)
	require.NoError(t, err)
	require.Positive(t, n, "empty binary fixture %s", path)
}

func writeAgentIntegrationConfig(t *testing.T, work string, port int) string {
	t.Helper()
	data := filepath.Join(work, "data")
	require.NoError(t, os.Mkdir(data, 0o700))
	config := filepath.Join(work, "cluster-config.json")
	// The Agent needs version metadata even with -binariesFixedPath. Match
	// the MongoDB Enterprise 8.0.4 amd64 binary supplied by the UBI9 image.
	quote := func(path string) string {
		encoded, err := json.Marshal(path)
		require.NoError(t, err)
		return string(encoded)
	}
	body := fmt.Sprintf(`{
	"version": 1,
	"auth": {"disabled": true},
	"options": {"downloadBase": %s},
	"mongoDbVersions": [{"name": "8.0.4", "builds": [{
		"architecture": "amd64", "bits": 64, "platform": "linux", "flavor": "rhel",
		"gitVersion": "bc35ab4305d9920d9d0491c1c9ef9b72383d31f9", "modules": ["enterprise"],
		"url": "https://downloads.mongodb.com/linux/mongodb-linux-x86_64-enterprise-rhel93-8.0.4.tgz"
	}]}],
	"processes": [{"name": "smoke", "hostname": "127.0.0.1", "processType": "mongod",
		"version": "8.0.4", "featureCompatibilityVersion": "8.0",
		"args2_6": {
			"net": {"bindIp": "127.0.0.1", "port": %d},
			"storage": {"dbPath": %s},
			"systemLog": {"destination": "file", "path": %s, "logAppend": true}
		}
	}]
}`, quote(work), port, quote(data), quote(filepath.Join(work, "mongod.log")))
	require.NoError(t, os.WriteFile(config, []byte(body), 0o600))
	return config
}
