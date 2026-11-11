// Package env provides a shared bootstrap for envtest-based Go tests: it starts
// a local Kubernetes control plane (etcd + kube-apiserver), installs the
// project's CRDs and returns a client connected to it.
//
// Each Go test package that needs an API server starts its own control plane:
// `go test` compiles every package into a separate binary, so a control plane
// cannot be shared across packages. Within a package, boot exactly once from
// TestMain via RunShared (the plain Go equivalent of Ginkgo's BeforeSuite) and
// access the environment from every test via Shared — this keeps the boot cost
// (a few seconds) at one per package no matter how many tests are added.
//
// The binaries are provisioned by `make envtest-assets`; the unit test entry
// points (make golang-tests, scripts/evergreen/unit-tests-golang.sh) run it
// automatically and export KUBEBUILDER_ASSETS.
package env

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kruntime "k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
)

// TestEnv is a running envtest control plane together with the means to talk to it.
type TestEnv struct {
	// Environment is the underlying envtest environment.
	Environment *envtest.Environment
	// Config is the rest.Config for the local API server; use it to build
	// managers or additional clients (e.g. in controller tests).
	Config *rest.Config
	// Client is a controller-runtime client with the mongodb.com/v1 scheme registered.
	Client client.Client
}

// Option customises the control plane before it starts.
type Option func(*envtest.Environment)

// WithCRDs restricts CRD installation to the given yaml files from
// config/crd/bases (e.g. "mongodb.com_mongodbsearch.yaml") instead of
// installing every CRD in the repository. Fewer CRDs means a faster boot.
func WithCRDs(crdFileNames ...string) Option {
	return func(e *envtest.Environment) {
		e.CRDDirectoryPaths = make([]string, 0, len(crdFileNames))
		for _, name := range crdFileNames {
			e.CRDDirectoryPaths = append(e.CRDDirectoryPaths, filepath.Join(crdBasesDir(), name))
		}
	}
}

// WithWebhooks installs the given webhook configuration files from
// config/webhooks (e.g. "webhooks.yaml"). envtest rewrites their service
// references to point at the local webhook server the test starts with StartWebhookServer.
func WithWebhooks(webhookFileNames ...string) Option {
	return func(e *envtest.Environment) {
		e.WebhookInstallOptions = envtest.WebhookInstallOptions{
			Paths:            make([]string, 0, len(webhookFileNames)),
			LocalServingHost: "127.0.0.1",
		}
		for _, name := range webhookFileNames {
			e.WebhookInstallOptions.Paths = append(e.WebhookInstallOptions.Paths, filepath.Join(repoRoot(), "config", "webhooks", name))
		}
	}
}

// shared is the package-wide environment started by RunShared.
var shared *TestEnv

// RunShared boots a single control plane shared by all tests in the package,
// runs the package's tests and tears the control plane down. It is the plain
// Go equivalent of Ginkgo's BeforeSuite/AfterSuite, used from the package's
// TestMain:
//
//	func TestMain(m *testing.M) {
//		os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbsearch.yaml")))
//	}
//
// Tests access the environment through Shared.
func RunShared(m *testing.M, opts ...Option) int {
	testEnv, err := start(opts...)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to start the envtest control plane; run `make envtest-assets` to download the required binaries: %v\n", err)
		return 1
	}
	shared = testEnv

	code := m.Run()

	if err := testEnv.Environment.Stop(); err != nil && code == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "failed to stop the envtest control plane: %v\n", err)
		return 1
	}
	return code
}

// Shared returns the package-wide environment started by RunShared in the
// package's TestMain. It fails the test immediately if RunShared was not used.
func Shared(t *testing.T) *TestEnv {
	t.Helper()
	require.NotNil(t, shared, "no shared envtest environment; call env.RunShared from the package's TestMain")
	return shared
}

// StartWebhookServer serves the validating webhooks that register adds to a manager, on
// the port envtest reserved through WithWebhooks. The server stops when the test ends:
// while it runs, it rejects the invalid objects the other tests of the package create.
func (e *TestEnv) StartWebhookServer(t *testing.T, register func(ctrl.Manager) error) {
	t.Helper()

	options := e.Environment.WebhookInstallOptions
	mgr, err := ctrl.NewManager(e.Config, ctrl.Options{
		Scheme: e.Client.Scheme(),
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    options.LocalServingHost,
			Port:    options.LocalServingPort,
			CertDir: options.LocalServingCertDir,
		}),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	require.NoError(t, register(mgr))

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := mgr.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("webhook manager failed: %v", err)
		}
	}()
	// Wait for the server to release the port, the next test starts its own on it.
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", options.LocalServingHost, options.LocalServingPort), time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 30*time.Second, 100*time.Millisecond, "webhook server did not become reachable")
}

// DeniedByWebhook is the error the API server reports when the named webhook denies a request.
func DeniedByWebhook(webhookName, message string) string {
	return fmt.Sprintf("admission webhook %q denied the request: %s", webhookName, message)
}

// start boots the control plane and builds the client; RunShared stops it
// after m.Run.
func start(opts ...Option) (*TestEnv, error) {
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{crdBasesDir()},
		ErrorIfCRDPathMissing: true,
		// Allow running tests directly (go test, IDE) without KUBEBUILDER_ASSETS,
		// as long as `make envtest-assets` has been run once.
		BinaryAssetsDirectory: binaryAssetsDir(),
	}
	for _, opt := range opts {
		opt(environment)
	}

	cfg, err := environment.Start()
	if err != nil {
		return nil, err
	}

	scheme := kruntime.NewScheme()
	if err := mdbv1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}

	return &TestEnv{Environment: environment, Config: cfg, Client: k8sClient}, nil
}

// crdBasesDir returns the absolute path to config/crd/bases.
func crdBasesDir() string {
	return filepath.Join(repoRoot(), "config", "crd", "bases")
}

// binaryAssetsDir returns the first version directory under bin/envtest/k8s
// provisioned by `make envtest-assets`, so that tests run directly (go test,
// IDE) work without extra setup. KUBEBUILDER_ASSETS, when set (the unit test
// entry points export it), takes precedence over this field inside envtest.
// An empty result leaves envtest's own defaults untouched.
func binaryAssetsDir() string {
	k8sDir := filepath.Join(repoRoot(), "bin", "envtest", "k8s")
	entries, err := os.ReadDir(k8sDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(k8sDir, entry.Name())
		}
	}
	return ""
}

// repoRoot returns the absolute path to the repository root, derived from this
// file's location so that tests work from any package, at any depth.
func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}
