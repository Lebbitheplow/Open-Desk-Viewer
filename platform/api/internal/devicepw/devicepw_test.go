package devicepw

import (
	"strings"
	"testing"
)

const testKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func TestParseKeyAcceptsEveryBase64Spelling(t *testing.T) {
	// The same 32 bytes written the four ways an operator's tooling produces
	// them. This key is all 0xff on purpose: its standard encoding is nothing
	// but '/' characters and its URL-safe encoding is nothing but '_', so the
	// two forms genuinely differ and the test is not four spellings of the same
	// string. Rejecting any of them would look to an operator like a corrupt
	// key rather than a picky decoder.
	want := strings.Repeat("\xff", KeyBytes)
	for name, encoded := range map[string]string{
		"standard, padded": strings.Repeat("/", 42) + "8=",
		"standard, raw":    strings.Repeat("/", 42) + "8",
		"url-safe, padded": strings.Repeat("_", 42) + "8=",
		"url-safe, raw":    strings.Repeat("_", 42) + "8",
	} {
		key, err := ParseKey(encoded)
		if err != nil {
			t.Fatalf("%s: ParseKey(%q): %v", name, encoded, err)
		}
		if string(key) != want {
			t.Fatalf("%s: ParseKey(%q) decoded to different bytes", name, encoded)
		}
	}
}

func TestParseKeyRejectsAnythingThatIsNot32Bytes(t *testing.T) {
	// A 16-byte key is the plausible mistake, and it would silently give
	// AES-128 where the documentation promises AES-256. Failing at startup is
	// the only moment an operator can act on it.
	for name, encoded := range map[string]string{
		"empty":      "",
		"not base64": "this is not base64 !!",
		"16 bytes":   "AAECAwQFBgcICQoLDA0ODw==",
		"31 bytes":   "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHg==",
		"33 bytes":   "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8g",
	} {
		if key, err := ParseKey(encoded); err == nil {
			t.Errorf("%s: ParseKey accepted %q and returned %d bytes", name, encoded, len(key))
		}
	}
}

func TestSealAndOpenRoundTrip(t *testing.T) {
	key, err := ParseKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, nonce, err := c.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == "hunter2" {
		t.Fatal("the ciphertext is the plaintext")
	}

	plaintext, err := c.Open(ciphertext, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "hunter2" {
		t.Fatalf("round trip gave %q", plaintext)
	}
}

// The property that matters for a nonce: sealing the same password twice must
// not produce the same bytes. If it did, anyone reading the table could see
// which devices share a password without decrypting anything.
func TestSealIsNotDeterministic(t *testing.T) {
	key, _ := ParseKey(testKey)
	c, _ := NewCipher(key)

	first, firstNonce, err := c.Seal("same-password")
	if err != nil {
		t.Fatal(err)
	}
	second, secondNonce, err := c.Seal("same-password")
	if err != nil {
		t.Fatal(err)
	}

	if string(first) == string(second) {
		t.Error("two seals of the same password produced identical ciphertext")
	}
	if string(firstNonce) == string(secondNonce) {
		t.Error("two seals reused the same nonce, which breaks GCM")
	}
}

func TestOpenRejectsTamperingAndTheWrongKey(t *testing.T) {
	key, _ := ParseKey(testKey)
	c, _ := NewCipher(key)
	ciphertext, nonce, _ := c.Seal("hunter2")

	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[0] ^= 0xff
	if _, err := c.Open(tampered, nonce); err == nil {
		t.Error("Open accepted a modified ciphertext; GCM's authentication is not being checked")
	}

	other, err := NewCipher([]byte(strings.Repeat("\xa5", KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(ciphertext, nonce); err == nil {
		t.Error("Open accepted a ciphertext sealed under a different key")
	}
}

func TestGenerateProducesDistinctPasswordsFromTheSafeAlphabet(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		password, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if len(password) != passwordLength {
			t.Fatalf("password %q is %d characters, want %d", password, len(password), passwordLength)
		}
		for _, r := range password {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("password %q contains %q, which is not in the alphabet", password, r)
			}
		}
		if seen[password] {
			t.Fatalf("Generate returned %q twice in 200 draws", password)
		}
		seen[password] = true
	}
}

// Applied is the field the portal renders as "this rotation has reached the
// device", so its three states are worth pinning: never confirmed, confirmed an
// older version, and confirmed this one.
func TestAppliedTracksTheConfirmedVersion(t *testing.T) {
	older := int64(1)
	current := int64(2)

	if (Password{Version: 2}).Applied() {
		t.Error("a password the device has never confirmed reports as applied")
	}
	if (Password{Version: 2, AppliedVersion: &older}).Applied() {
		t.Error("a device still on version 1 reports version 2 as applied")
	}
	if !(Password{Version: 2, AppliedVersion: &current}).Applied() {
		t.Error("a device confirming the current version does not report as applied")
	}
}
