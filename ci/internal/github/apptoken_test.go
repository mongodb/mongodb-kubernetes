package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func pkcs1PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestParseRSAPrivateKey(t *testing.T) {
	key := testKey(t)

	t.Run("pkcs1", func(t *testing.T) {
		if _, err := parseRSAPrivateKey(pkcs1PEM(t, key)); err != nil {
			t.Fatalf("pkcs1: %v", err)
		}
	})
	t.Run("pkcs8", func(t *testing.T) {
		if _, err := parseRSAPrivateKey(pkcs8PEM(t, key)); err != nil {
			t.Fatalf("pkcs8: %v", err)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		if _, err := parseRSAPrivateKey([]byte("not a pem")); err == nil {
			t.Fatal("expected error for non-PEM input")
		}
	})
}

func TestBuildAppJWT(t *testing.T) {
	key := testKey(t)
	now := time.Unix(1_700_000_000, 0)

	jwt, err := buildAppJWT("12345", key, now)
	if err != nil {
		t.Fatalf("buildAppJWT: %v", err)
	}

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT segments, got %d", len(parts))
	}
	enc := base64.RawURLEncoding

	// Verify the signature against the header.payload signing input.
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}

	// Verify the claims: iss, and iat backdated 60s, exp 9m out.
	claimsJSON, err := enc.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Iss != "12345" {
		t.Errorf("iss = %q, want 12345", claims.Iss)
	}
	if want := now.Add(-60 * time.Second).Unix(); claims.Iat != want {
		t.Errorf("iat = %d, want %d", claims.Iat, want)
	}
	if want := now.Add(9 * time.Minute).Unix(); claims.Exp != want {
		t.Errorf("exp = %d, want %d", claims.Exp, want)
	}
}

func TestMintInstallationToken(t *testing.T) {
	key := testKey(t)

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/app/installations/678/access_tokens" {
				t.Errorf("unexpected path %q", r.URL.Path)
			}
			if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
				t.Errorf("missing bearer auth, got %q", auth)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_faketoken"}`))
		}))
		defer srv.Close()

		token, err := MintInstallationToken(context.Background(), AppTokenInputs{
			AppID:          "12345",
			InstallationID: "678",
			PEM:            pkcs1PEM(t, key),
			APIBaseURL:     srv.URL,
		})
		if err != nil {
			t.Fatalf("MintInstallationToken: %v", err)
		}
		if token != "ghs_faketoken" {
			t.Errorf("token = %q, want ghs_faketoken", token)
		}
	})

	t.Run("http error surfaces body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"bad creds"}`))
		}))
		defer srv.Close()

		_, err := MintInstallationToken(context.Background(), AppTokenInputs{
			AppID:          "12345",
			InstallationID: "678",
			PEM:            pkcs1PEM(t, key),
			APIBaseURL:     srv.URL,
		})
		if err == nil || !strings.Contains(err.Error(), "bad creds") {
			t.Fatalf("expected error containing response body, got %v", err)
		}
	})

	t.Run("missing inputs", func(t *testing.T) {
		_, err := MintInstallationToken(context.Background(), AppTokenInputs{})
		if err == nil {
			t.Fatal("expected error for empty inputs")
		}
	})
}
