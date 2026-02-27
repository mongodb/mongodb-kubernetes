package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/migration/connectivitycheck"
)

func main() {
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
		_, _ = fmt.Fprintln(os.Stderr, "CONNECTION_STRING is required")
		os.Exit(connectivitycheck.ExitUnknown)
	}
	os.Exit(connectivitycheck.Validate(context.Background(), cfg))
}
