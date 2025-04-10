package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/xerrors"
)

const (
	healthEndpointPortEnv = "HEALTH_ENDPOINT_PORT"
)

// HealthResponse represents the response given from the backup daemon health endpoint.
// Sample responses:
// - {"sync_db":"OK","backup_db":"OK","mms_db":"OK"}
// - {"backup_db":"no master","mms_db":"no master"}
type HealthResponse struct {
	SyncDb   string `json:"sync_db"`
	BackupDb string `json:"backup_db"`
	MmsDb    string `json:"mms_db"`
}

func main() {
	os.Exit(checkHealthEndpoint(&http.Client{
		Timeout: 5 * time.Second,
	}))
}

// checkHealthEndpoint checks the BackupDaemon health endpoint
// and ensures that the AppDB is up and running.
func checkHealthEndpoint(getter httpGetter) int {
	hr, err := getHealthResponse(getter)
	if err != nil {
		fmt.Printf("error getting health response: %s\n", err)
		return 1
	}

	fmt.Printf("received response: %+v\n", hr)
	if hr.MmsDb == "OK" {
		return 0
	}
	return 1
}

type httpGetter interface {
	Get(url string) (*http.Response, error)
}

// getHealthResponse fetches the health response from the health endpoint.
func getHealthResponse(getter httpGetter) (HealthResponse, error) {
	url := fmt.Sprintf("http://localhost:%s/health", os.Getenv(healthEndpointPortEnv)) // nolint:forbidigo
	fmt.Printf("attempting GET request to: [%s]\n", url)
	resp, err := getter.Get(url)
	if err != nil {
		return HealthResponse{}, xerrors.Errorf("failed to reach health endpoint: %w", err)
	}

	if resp.StatusCode != 200 {
		return HealthResponse{}, xerrors.Errorf("received status code [%d] but expected [200]", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return HealthResponse{}, xerrors.Errorf("failed to read response body: %w", err)
	}

	hr := HealthResponse{}
	if err := json.Unmarshal(body, &hr); err != nil {
		return HealthResponse{}, xerrors.Errorf("failed to unmarshal response: %w", err)
	}

	return hr, nil
}
