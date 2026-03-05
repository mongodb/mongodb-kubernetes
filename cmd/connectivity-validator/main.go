package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/migration/connectivitycheck"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// syncAfterWriteWriter flushes to the OS after every Write so container logs (e.g. kubectl logs)
// show output even when stdout is fully buffered and the process exits quickly.
type syncAfterWriteWriter struct{ io.Writer }

func (w syncAfterWriteWriter) Write(p []byte) (n int, err error) {
	n, err = w.Writer.Write(p)
	if err == nil {
		if s, ok := w.Writer.(interface{ Sync() error }); ok {
			_ = s.Sync()
		}
	}
	return n, err
}

const startMsg = "connectivity-validator starting\n"

func main() {
	// Write to fd 1 and 2 via syscall so output is visible in kubectl logs even when
	// the Go runtime or libc buffers stdout/stderr (e.g. non-TTY in containers).
	_, _ = syscall.Write(1, []byte(startMsg))
	_, _ = syscall.Write(2, []byte(startMsg))
	// Also write to the pod termination log so it appears in kubectl describe pod.
	if f, err := os.OpenFile("/dev/termination-log", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err == nil {
		_, _ = f.Write([]byte(startMsg))
		_ = f.Close()
	}

	stdout := syncAfterWriteWriter{Writer: os.Stdout}
	_, _ = fmt.Fprintf(stdout, startMsg)

	// All logs to stdout (12-factor / common production pattern). Level is in the payload for filtering.
	encoder := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(stdout), zapcore.DebugLevel)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	defer func() { _ = logger.Sync() }()
	zap.ReplaceGlobals(logger)

	members := strings.Fields(os.Getenv("EXTERNAL_MEMBERS"))
	cfg := connectivitycheck.Config{
		ConnectionString: os.Getenv("CONNECTION_STRING"),
		ExternalMembers:  members,
		AuthMechanism:    os.Getenv("AUTH_MECHANISM"),
		KeyfilePath:      os.Getenv("KEYFILE_PATH"),
		CertPath:         os.Getenv("CERT_PATH"),
		CAPath:           os.Getenv("CA_PATH"),
		SubjectDN:        os.Getenv("SUBJECT_DN"),
	}
	if cfg.ConnectionString == "" {
		zap.S().Error("CONNECTION_STRING is required")
		_, _ = fmt.Fprintln(os.Stderr, "CONNECTION_STRING is required")
		os.Exit(connectivitycheck.ExitUnknown)
	}

	exitCode := connectivitycheck.Validate(context.Background(), cfg)
	zap.S().Infow("Connectivity validation finished", "exitCode", exitCode, "exitCodeName", connectivitycheck.ExitCodeName(exitCode))
	os.Exit(exitCode)
}
