package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const GitHubAPIBaseURL = "https://api.github.com"

// AppTokenInputs are the parameters needed to mint an installation token.
type AppTokenInputs struct {
	AppID          string
	InstallationID string
	PEM            []byte

	APIBaseURL string // override for tests
	HTTPClient *http.Client
}

// MintInstallationToken exchanges an app JWT for an installation access token.
func MintInstallationToken(ctx context.Context, in AppTokenInputs) (string, error) {
	if in.AppID == "" {
		return "", errors.New("app-id is required")
	}
	if in.InstallationID == "" {
		return "", errors.New("installation-id is required")
	}
	if len(in.PEM) == 0 {
		return "", errors.New("pem is required")
	}

	key, err := parseRSAPrivateKey(in.PEM)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}

	jwt, err := buildAppJWT(in.AppID, key, time.Now())
	if err != nil {
		return "", fmt.Errorf("build app jwt: %w", err)
	}

	baseURL := in.APIBaseURL
	if baseURL == "" {
		baseURL = GitHubAPIBaseURL
	}
	client := in.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", baseURL, in.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("exchange jwt for installation token: %s: %s", resp.Status, bytes.TrimSpace(body))
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("token missing from GitHub response")
	}
	return out.Token, nil
}

// parseRSAPrivateKey accepts PKCS#1 or PKCS#8 PEM.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found (is the key base64-decoded correctly?)")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a valid PKCS#1 or PKCS#8 RSA key: %w", err)
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("unexpected key type %T, want RSA", keyAny)
	}
	return key, nil
}

// buildAppJWT signs a 10-minute RS256 JWT, backdated 60s for clock skew.
func buildAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}
