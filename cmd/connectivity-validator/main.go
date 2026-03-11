package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/migration/connectivitycheck"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	_, _ = fmt.Fprintf(os.Stdout, "connectivity-validator starting\n")

	// All logs to stdout. Console encoder is readable in kubectl logs; use ProductionEncoderConfig() for JSON.
	encoder := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), zapcore.DebugLevel)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	defer func() { _ = logger.Sync() }()
	zap.ReplaceGlobals(logger) // validator package uses zap.S()
	log := logger.Sugar()

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
		log.Error("CONNECTION_STRING is required")
		_, _ = fmt.Fprintln(os.Stderr, "CONNECTION_STRING is required")
		os.Exit(connectivitycheck.ExitUnknown)
	}

	exitCode := connectivitycheck.Validate(context.Background(), cfg)
	log.Infow("Connectivity validation finished", "exitCode", exitCode, "exitCodeName", connectivitycheck.ExitCodeName(exitCode))
	os.Exit(exitCode)
}
