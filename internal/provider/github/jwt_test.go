package github_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/provider/github"
)

func generateTestKeyPEMs(t *testing.T) (pkcs1PEM, pkcs8PEM string, pubKey *rsa.PublicKey) {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating rsa key: %v", err)
	}

	pkcs1Bytes := x509.MarshalPKCS1PrivateKey(privKey)
	pkcs1Block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1Bytes}
	pkcs1PEM = string(pem.EncodeToMemory(pkcs1Block))

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		t.Fatalf("marshalling pkcs8: %v", err)
	}
	pkcs8Block := &pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes}
	pkcs8PEM = string(pem.EncodeToMemory(pkcs8Block))

	return pkcs1PEM, pkcs8PEM, &privKey.PublicKey
}

func TestParseRSAPrivateKey(t *testing.T) {
	pkcs1PEM, pkcs8PEM, _ := generateTestKeyPEMs(t)

	// PKCS#1
	k1, err := github.ParseRSAPrivateKey([]byte(pkcs1PEM))
	if err != nil {
		t.Fatalf("parsing pkcs1 failed: %v", err)
	}
	if k1 == nil {
		t.Fatal("expected non-nil pkcs1 key")
	}

	// PKCS#8
	k2, err := github.ParseRSAPrivateKey([]byte(pkcs8PEM))
	if err != nil {
		t.Fatalf("parsing pkcs8 failed: %v", err)
	}
	if k2 == nil {
		t.Fatal("expected non-nil pkcs8 key")
	}

	// Invalid
	if _, err := github.ParseRSAPrivateKey([]byte("not a pem key")); err == nil {
		t.Fatal("expected error on invalid PEM")
	}
}

func TestGenerateAppJWT(t *testing.T) {
	pkcs1PEM, _, pubKey := generateTestKeyPEMs(t)
	privKey, err := github.ParseRSAPrivateKey([]byte(pkcs1PEM))
	if err != nil {
		t.Fatalf("parsing key: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	appID := int64(123456)
	jwtStr, err := github.GenerateAppJWT(appID, privKey, now)
	if err != nil {
		t.Fatalf("generating JWT: %v", err)
	}

	parts := strings.Split(jwtStr, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts in JWT, got %d", len(parts))
	}

	// Verify Header
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decoding header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("unmarshalling header: %v", err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("unexpected header: %v", header)
	}

	// Verify Claims
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatalf("unmarshalling claims: %v", err)
	}
	if claims["iss"] != "123456" {
		t.Errorf("expected iss 123456, got %v", claims["iss"])
	}

	// Verify RS256 signature
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decoding sig: %v", err)
	}
	signedData := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signedData))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hash[:], sig); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}
}
