package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://keycloak.test/realms/odv"
	testAudience = "odv-api"
)

// jwksServer serves a JWKS document whose keys can be rotated mid-test.
type jwksServer struct {
	*httptest.Server
	mu    sync.Mutex
	keys  map[string]*rsa.PublicKey
	fetch int
}

func newJWKSServer(t *testing.T, keys map[string]*rsa.PublicKey) *jwksServer {
	t.Helper()

	s := &jwksServer{keys: keys}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.fetch++

		doc := JWKSResponse{}
		for kid, pub := range s.keys {
			doc.Keys = append(doc.Keys, JWK{
				Kty: "RSA",
				Kid: kid,
				Use: "sig",
				N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(s.Close)

	return s
}

func (s *jwksServer) rotate(keys map[string]*rsa.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

func (s *jwksServer) fetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetch
}

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	return key
}

// validClaims returns a claim set that passes every check, for tests to spoil
// one field at a time.
func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub":                "sub-1",
		"iss":                testIssuer,
		"aud":                testAudience,
		"exp":                time.Now().Add(time.Hour).Unix(),
		"iat":                time.Now().Unix(),
		"email":              "tech@example.com",
		"preferred_username": "tech",
		"name":               "Tech User",
	}
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return signed
}

func newValidator(jwksURL string) *JWTValidator {
	return NewJWTValidator(
		NewJWKSProvider(jwksURL),
		WithIssuer(testIssuer),
		WithAudience(testAudience),
		WithAlgorithm("RS256"),
	)
}

// newTestValidator wires a validator against a single signing key.
func newTestValidator(t *testing.T) (*JWTValidator, *rsa.PrivateKey) {
	t.Helper()
	key := newKey(t)
	server := newJWKSServer(t, map[string]*rsa.PublicKey{"kid-1": &key.PublicKey})
	return newValidator(server.URL), key
}

func TestValidateTokenAcceptsAWellFormedToken(t *testing.T) {
	validator, key := newTestValidator(t)

	claims, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-1", validClaims()))
	if err != nil {
		t.Fatalf("expected the token to be accepted, got %v", err)
	}

	if claims.Subject != "sub-1" {
		t.Errorf("expected subject sub-1, got %q", claims.Subject)
	}
	if claims.Issuer != testIssuer {
		t.Errorf("expected issuer %q, got %q", testIssuer, claims.Issuer)
	}
	if claims.Audience != testAudience {
		t.Errorf("expected audience %q, got %q", testAudience, claims.Audience)
	}
}

// The email and username claims drive just-in-time user provisioning, so they
// have to survive the round trip.
func TestValidateTokenSurfacesProfileClaims(t *testing.T) {
	validator, key := newTestValidator(t)

	claims, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-1", validClaims()))
	if err != nil {
		t.Fatalf("expected the token to be accepted, got %v", err)
	}

	if claims.Email != "tech@example.com" {
		t.Errorf("expected email tech@example.com, got %q", claims.Email)
	}
	if claims.PreferredUsername != "tech" {
		t.Errorf("expected preferred_username tech, got %q", claims.PreferredUsername)
	}
	if claims.Name != "Tech User" {
		t.Errorf("expected name 'Tech User', got %q", claims.Name)
	}
}

func TestValidateTokenRejectsAForgedSignature(t *testing.T) {
	validator, key := newTestValidator(t)

	// Signed by a key the issuer never published.
	attacker := newKey(t)
	forged := signRS256(t, attacker, "kid-1", validClaims())

	if _, err := validator.ValidateToken(context.Background(), forged); err == nil {
		t.Fatal("expected a token signed by an unknown key to be rejected")
	}

	// Same payload, real key, tampered signature.
	genuine := signRS256(t, key, "kid-1", validClaims())
	parts := strings.Split(genuine, ".")
	tampered := parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2]))

	if _, err := validator.ValidateToken(context.Background(), tampered); err == nil {
		t.Fatal("expected a tampered signature to be rejected")
	}
}

func TestValidateTokenRejectsTheNoneAlgorithm(t *testing.T) {
	validator, _ := newTestValidator(t)

	token := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims())
	token.Header["kid"] = "kid-1"
	unsigned, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("failed to build an unsigned token: %v", err)
	}

	if _, err := validator.ValidateToken(context.Background(), unsigned); err == nil {
		t.Fatal("expected alg:none to be rejected")
	}
}

// Algorithm confusion: an HMAC token whose key is the public modulus.
func TestValidateTokenRejectsHMACSignedTokens(t *testing.T) {
	key := newKey(t)
	server := newJWKSServer(t, map[string]*rsa.PublicKey{"kid-1": &key.PublicKey})
	validator := newValidator(server.URL)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims())
	token.Header["kid"] = "kid-1"
	confused, err := token.SignedString(key.PublicKey.N.Bytes())
	if err != nil {
		t.Fatalf("failed to build an HMAC token: %v", err)
	}

	if _, err := validator.ValidateToken(context.Background(), confused); err == nil {
		t.Fatal("expected an HS256 token to be rejected by an RS256 validator")
	}
}

func TestValidateTokenRejectsExpiredTokens(t *testing.T) {
	validator, key := newTestValidator(t)

	claims := validClaims()
	claims["exp"] = time.Now().Add(-time.Minute).Unix()

	if _, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-1", claims)); err == nil {
		t.Fatal("expected an expired token to be rejected")
	}
}

// A token with no exp used to reach an unguarded ExpiresAt.Unix() and panic.
func TestValidateTokenRejectsTokensWithoutExpiry(t *testing.T) {
	validator, key := newTestValidator(t)

	claims := validClaims()
	delete(claims, "exp")

	_, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-1", claims))
	if err == nil {
		t.Fatal("expected a token without exp to be rejected")
	}
}

func TestValidateTokenRejectsTheWrongIssuer(t *testing.T) {
	validator, key := newTestValidator(t)

	claims := validClaims()
	claims["iss"] = "https://evil.test/realms/odv"

	if _, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-1", claims)); err == nil {
		t.Fatal("expected a token from another issuer to be rejected")
	}
}

func TestValidateTokenRejectsTheWrongAudience(t *testing.T) {
	validator, key := newTestValidator(t)

	claims := validClaims()
	claims["aud"] = "some-other-client"

	if _, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-1", claims)); err == nil {
		t.Fatal("expected a token for another audience to be rejected")
	}
}

func TestValidateTokenRejectsTokensWithoutASubject(t *testing.T) {
	validator, key := newTestValidator(t)

	claims := validClaims()
	delete(claims, "sub")

	if _, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-1", claims)); err == nil {
		t.Fatal("expected a token without a subject to be rejected")
	}
}

// An unknown kid must trigger a JWKS refetch, so a key rotation does not black
// out authentication until the cache expires.
func TestValidateTokenRefreshesJWKSOnUnknownKid(t *testing.T) {
	oldKey := newKey(t)
	server := newJWKSServer(t, map[string]*rsa.PublicKey{"kid-1": &oldKey.PublicKey})
	validator := newValidator(server.URL)

	// Warm the cache on the old key.
	if _, err := validator.ValidateToken(context.Background(), signRS256(t, oldKey, "kid-1", validClaims())); err != nil {
		t.Fatalf("expected the pre-rotation token to be accepted, got %v", err)
	}
	fetchesBefore := server.fetchCount()

	newKeyPair := newKey(t)
	server.rotate(map[string]*rsa.PublicKey{"kid-2": &newKeyPair.PublicKey})

	if _, err := validator.ValidateToken(context.Background(), signRS256(t, newKeyPair, "kid-2", validClaims())); err != nil {
		t.Fatalf("expected the post-rotation token to be accepted, got %v", err)
	}

	if server.fetchCount() <= fetchesBefore {
		t.Error("expected an unknown kid to force a JWKS refetch")
	}
}

func TestValidateTokenRejectsAnUnknownKidAfterRefresh(t *testing.T) {
	validator, key := newTestValidator(t)

	if _, err := validator.ValidateToken(context.Background(), signRS256(t, key, "kid-unknown", validClaims())); err == nil {
		t.Fatal("expected a token with an unpublished kid to be rejected")
	}
}
