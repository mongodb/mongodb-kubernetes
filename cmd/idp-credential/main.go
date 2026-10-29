// Disclaimer: this file was mostly generated with AI, as part of a POC.

// Command idp-credential is a client-go exec credential plugin for the
// IdP-federation POC. client-go invokes it (per the kubeconfig `exec:` user
// entry) whenever a member-cluster client needs a bearer token; it fetches a
// short-lived OAuth2 client-credentials token from a central IdP and prints it
// as a client.authentication.k8s.io/v1 ExecCredential on stdout. client-go then
// caches the token and re-invokes this helper when expirationTimestamp passes,
// which is what gives the operator transparent refresh across token expiry.
//
// This is intentionally POC-grade:
//   - client_credentials grant only (one shared operator identity)
//   - the client secret comes from a file/flag/env; the mock IdP accepts any
//     value, so the POC does not exercise real client authentication
//   - --insecure-skip-verify skips TLS verification of the IdP, matching the
//     POC's self-signed IdP certificate (production would mount the IdP CA)
//
// The secret and the token are never written to stderr, because client-go
// surfaces an exec plugin's stderr in the operator's logs.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ExecCredential mirrors client.authentication.k8s.io/v1 ExecCredential. It is
// defined inline to keep the helper dependency-light (stdlib only).
type ExecCredential struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Status     ExecStatus `json:"status"`
}

// ExecStatus is the status block client-go reads. expirationTimestamp is what
// makes client-go treat the token as expiring and refresh it; without it the
// credential would be cached for the lifetime of the operator process.
type ExecStatus struct {
	Token               string `json:"token"`
	ExpirationTimestamp string `json:"expirationTimestamp,omitempty"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func main() {
	var (
		tokenURL   = flag.String("token-url", "", "IdP OAuth2 token endpoint (env IDP_TOKEN_URL)")
		clientID   = flag.String("client-id", "", "OAuth2 client id; also becomes the token 'sub' on the mock IdP (env IDP_CLIENT_ID)")
		secret     = flag.String("client-secret", "", "OAuth2 client secret (env IDP_CLIENT_SECRET)")
		secretFile = flag.String("client-secret-file", "", "path to a file holding the client secret; preferred over the secret value (env IDP_CLIENT_SECRET_FILE)")
		scope      = flag.String("scope", "", "OAuth2 scope; on the mock IdP this becomes the token audience and must equal the member API server's --oidc-client-id (env IDP_SCOPE)")
		audience   = flag.String("audience", "", "optional audience parameter for IdPs that honour it (env IDP_AUDIENCE)")
		insecure   = flag.Bool("insecure-skip-verify", false, "skip TLS verification of the IdP (POC only; env IDP_INSECURE_SKIP_VERIFY)")
	)
	flag.Parse()

	// Flags win over env; env lets the kubeconfig exec `env:` block carry config
	// without putting it in `args:`.
	*tokenURL = firstNonEmpty(*tokenURL, os.Getenv("IDP_TOKEN_URL"))
	*clientID = firstNonEmpty(*clientID, os.Getenv("IDP_CLIENT_ID"))
	*scope = firstNonEmpty(*scope, os.Getenv("IDP_SCOPE"))
	*audience = firstNonEmpty(*audience, os.Getenv("IDP_AUDIENCE"))
	if v := os.Getenv("IDP_INSECURE_SKIP_VERIFY"); v != "" {
		*insecure = v == "true" || v == "1"
	}

	*secretFile = firstNonEmpty(*secretFile, os.Getenv("IDP_CLIENT_SECRET_FILE"))
	if *secretFile != "" {
		b, err := os.ReadFile(*secretFile)
		if err != nil {
			fatal("reading client secret file: %v", err)
		}
		*secret = strings.TrimSpace(string(b))
	} else {
		*secret = firstNonEmpty(*secret, os.Getenv("IDP_CLIENT_SECRET"))
	}

	if *tokenURL == "" || *clientID == "" {
		fatal("token-url and client-id are required (via flags or IDP_TOKEN_URL/IDP_CLIENT_ID)")
	}

	token, expiry, err := fetchToken(*tokenURL, *clientID, *secret, *scope, *audience, *insecure)
	if err != nil {
		fatal("fetching token: %v", err)
	}

	out := ExecCredential{
		APIVersion: "client.authentication.k8s.io/v1",
		Kind:       "ExecCredential",
		Status:     ExecStatus{Token: token},
	}
	if !expiry.IsZero() {
		out.Status.ExpirationTimestamp = expiry.UTC().Format(time.RFC3339)
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fatal("encoding ExecCredential: %v", err)
	}
}

// fetchToken performs the client_credentials request and returns the access
// token plus its absolute expiry (now + expires_in).
func fetchToken(tokenURL, clientID, secret, scope, audience string, insecure bool) (string, time.Time, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	if scope != "" {
		form.Set("scope", scope)
	}
	if audience != "" {
		form.Set("audience", audience)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	if insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // nolint:gosec // POC only: self-signed IdP cert
		}
	}

	resp, err := client.PostForm(tokenURL, form)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func(Body io.ReadCloser) { _ = Body.Close() }(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}

	// Best-effort parse so we can surface a structured IdP error without echoing
	// the raw body (which could carry sensitive data on a real IdP).
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr)

	if resp.StatusCode != http.StatusOK {
		if tr.Error != "" {
			return "", time.Time{}, fmt.Errorf("token endpoint returned %s: %s: %s", resp.Status, tr.Error, tr.ErrorDescription)
		}
		return "", time.Time{}, fmt.Errorf("token endpoint returned %s", resp.Status)
	}
	if tr.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("token response contained no access_token")
	}

	expiry := time.Now().Add(time.Hour) // default when the IdP omits expires_in
	if tr.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return tr.AccessToken, expiry, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "idp-credential: "+format+"\n", args...)
	os.Exit(1)
}
