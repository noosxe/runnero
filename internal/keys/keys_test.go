package keys

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// testMaster is a 32-byte placeholder master key (the same floor
// config.MinEncryptionKeyBytes enforces) used by every test below.
const testMaster = "0123456789abcdef0123456789abcdef"

// Known-answer vectors (HKDF-SHA256, nil salt, raw master-key bytes as
// input keying material). They pin the derivation so that any accidental
// change to the labels, hash, salt handling, or key sizes breaks the build
// instead of silently invalidating every encrypted database row and
// issued session token across a binary upgrade.
const (
	katDBEncryptionHex  = "3684ac170cfa0c6909be5d6862939df14b4f149f43b86cd9de5e56b3b48a589a"
	katJWTSigningHex    = "67803b0336e64fbb34facfa8038b2cc17d98fa54744ac6bbe4747aa1806fbac0"
	katOtherMasterValue = "fedcba9876543210fedcba9876543210"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding hex %q: %v", s, err)
	}
	return b
}

// TestDeriveDeterministic proves the same master key always yields the
// same secrets — the property that keeps ciphertexts and issued JWTs valid
// across restarts — via two independent Derive calls and a direct
// HKDF-Key recomputation.
func TestDeriveDeterministic(t *testing.T) {
	first, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("first Derive: %v", err)
	}
	second, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("second Derive: %v", err)
	}
	if !bytes.Equal(first.DBEncryptionKey, second.DBEncryptionKey) {
		t.Error("database encryption key differs between two derivations of the same master key")
	}
	if !bytes.Equal(first.JWTSigningSecret, second.JWTSigningSecret) {
		t.Error("JWT signing secret differs between two derivations of the same master key")
	}

	// Independent path: the package must agree with a bare hkdf.Key call
	// using the documented parameters (SHA-256, nil salt, raw master-key
	// bytes, exported labels).
	raw, err := hkdf.Key(sha256.New, []byte(testMaster), nil, LabelDBEncryption, DBEncryptionKeySize)
	if err != nil {
		t.Fatalf("recomputing database encryption key with hkdf.Key: %v", err)
	}
	if !bytes.Equal(first.DBEncryptionKey, raw) {
		t.Errorf("database encryption key %x does not match direct hkdf.Key output %x", first.DBEncryptionKey, raw)
	}
}

// TestDeriveKnownAnswer checks both secrets against the pinned vectors.
func TestDeriveKnownAnswer(t *testing.T) {
	d, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if want := mustHex(t, katDBEncryptionHex); !bytes.Equal(d.DBEncryptionKey, want) {
		t.Errorf("database encryption key = %x, want %x", d.DBEncryptionKey, want)
	}
	if want := mustHex(t, katJWTSigningHex); !bytes.Equal(d.JWTSigningSecret, want) {
		t.Errorf("JWT signing secret = %x, want %x", d.JWTSigningSecret, want)
	}
}

// TestDerivedKeysAreIndependent proves label separation (RUN-9
// acceptance: two independent keys from one master): the two secrets
// derived from the same master are unrelated, different masters produce
// unrelated secrets, and no output crosses over.
func TestDerivedKeysAreIndependent(t *testing.T) {
	if LabelDBEncryption == LabelJWTSigning {
		t.Fatalf("context labels are identical: %q", LabelDBEncryption)
	}

	a, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("Derive(master A): %v", err)
	}
	b, err := Derive(katOtherMasterValue)
	if err != nil {
		t.Fatalf("Derive(master B): %v", err)
	}

	if bytes.Equal(a.DBEncryptionKey, a.JWTSigningSecret) {
		t.Error("database encryption key and JWT signing secret are identical — labels failed to separate outputs")
	}
	if bytes.Equal(a.DBEncryptionKey, b.DBEncryptionKey) {
		t.Error("different master keys produced the same database encryption key")
	}
	if bytes.Equal(a.JWTSigningSecret, b.JWTSigningSecret) {
		t.Error("different master keys produced the same JWT signing secret")
	}
	// Cross-products: a label's output for one master must not appear
	// under any other label/master combination.
	if bytes.Equal(a.JWTSigningSecret, b.DBEncryptionKey) {
		t.Error("JWT signing secret of master A equals database encryption key of master B")
	}
	if bytes.Equal(a.DBEncryptionKey, b.JWTSigningSecret) {
		t.Error("database encryption key of master A equals JWT signing secret of master B")
	}

	// Label entropy, not key length, is what separates the outputs: the
	// same label with a different purpose prefix must differ.
	dbA, err := hkdf.Key(sha256.New, []byte(testMaster), nil, LabelDBEncryption, DBEncryptionKeySize)
	if err != nil {
		t.Fatalf("hkdf.Key(db label): %v", err)
	}
	jwtA, err := hkdf.Key(sha256.New, []byte(testMaster), nil, LabelJWTSigning, JWTSigningKeySize)
	if err != nil {
		t.Fatalf("hkdf.Key(jwt label): %v", err)
	}
	if bytes.Equal(dbA, jwtA) {
		t.Error("hkdf outputs under distinct labels are identical")
	}
}

// TestDeriveKeySizes checks the documented output sizes: 32-byte AES-256
// database key and 32-byte HMAC-SHA-256 signing secret.
func TestDeriveKeySizes(t *testing.T) {
	d, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	// AES-256 needs a 32-byte key; pin the exported constants too so a
	// careless resize fails here rather than at encryption time.
	if DBEncryptionKeySize != 32 || JWTSigningKeySize != 32 {
		t.Fatalf("exported key sizes drifted: db=%d jwt=%d, want 32/32", DBEncryptionKeySize, JWTSigningKeySize)
	}
	if got := len(d.DBEncryptionKey); got != DBEncryptionKeySize {
		t.Errorf("database encryption key size = %d, want %d (AES-256)", got, DBEncryptionKeySize)
	}
	if got := len(d.JWTSigningSecret); got != JWTSigningKeySize {
		t.Errorf("JWT signing secret size = %d, want %d", got, JWTSigningKeySize)
	}
}

// TestDeriveRejectsWeakMasterKey proves weak input is refused even without
// configuration validation: one byte short is already too weak, and the
// error names the environment variable that fixes it.
func TestDeriveRejectsWeakMasterKey(t *testing.T) {
	for name, master := range map[string]string{
		"empty":      "",
		"31 bytes":   "0123456789abcdef0123456789abcde",
		"short text": "hunter2",
	} {
		d, err := Derive(master)
		if err == nil {
			t.Errorf("%s: Derive succeeded, want error", name)
			continue
		}
		if d != nil {
			t.Errorf("%s: Derive returned non-nil Derived alongside error", name)
		}
		for _, want := range []string{"master key", "SUPERVISOR_DB_ENCRYPTION_KEY"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not mention %q", name, err.Error(), want)
			}
		}
	}

	// A master at exactly the floor is accepted.
	if _, err := Derive(strings.Repeat("x", MinMasterKeyBytes)); err != nil {
		t.Errorf("Derive at the minimum size failed: %v", err)
	}
}
