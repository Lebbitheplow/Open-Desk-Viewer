package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/golang-jwt/jwt/v5"
)

// JWKSResponse is the structure of a JWKS document
type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

// JWK is a JSON Web Key
type JWK struct {
	Kty string   `json:"kty"`
	Kid string   `json:"kid"`
	Use string   `json:"use"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5c []string `json:"x5c"`
}

// Claims represents the JWT claims we care about on top of the registered set.
// Do not redeclare registered claims such as sub or aud here: a shadowing field
// wins at decode time and leaves the embedded value empty, which silently
// disables the library's own validation of it.
type Claims struct {
	jwt.RegisteredClaims
	Email             string   `json:"email,omitempty"`
	PreferredUsername string   `json:"preferred_username,omitempty"`
	Name              string   `json:"name,omitempty"`
	Roles             []string `json:"roles,omitempty"`
}

// JWKSProvider fetches and caches the JWKS from Keycloak
type JWKSProvider struct {
	jwksURL    string
	mu         sync.RWMutex
	cache      *JWKSResponse
	cacheAt    time.Time
	cacheTTL   time.Duration
	httpClient *http.Client
}

// NewJWKSProvider creates a new JWKS provider
func NewJWKSProvider(jwksURL string) *JWKSProvider {
	return &JWKSProvider{
		jwksURL:  jwksURL,
		cacheTTL: 5 * time.Minute,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetJWKS fetches or returns cached JWKS
func (p *JWKSProvider) GetJWKS(ctx context.Context) (*JWKSResponse, error) {
	p.mu.RLock()
	if p.cache != nil && time.Since(p.cacheAt) < p.cacheTTL {
		cache := *p.cache
		p.mu.RUnlock()
		return &cache, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if p.cache != nil && time.Since(p.cacheAt) < p.cacheTTL {
		cache := *p.cache
		return &cache, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read JWKS response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var jwks JWKSResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("failed to parse JWKS: %w", err)
	}

	p.cache = &jwks
	p.cacheAt = time.Now()
	return &jwks, nil
}

// Refresh discards the cache and fetches the JWKS again. Used when a token
// presents an unknown kid, so a key rotation does not black out authentication
// until the cache expires.
func (p *JWKSProvider) Refresh(ctx context.Context) (*JWKSResponse, error) {
	p.mu.Lock()
	p.cacheAt = time.Time{}
	p.mu.Unlock()
	return p.GetJWKS(ctx)
}

// JWTValidator validates OIDC access tokens from Keycloak
type JWTValidator struct {
	provider *JWKSProvider
	issuer   string
	audience string
	alg      string
}

// ValidatorOption configures a JWTValidator
type ValidatorOption func(*JWTValidator)

// WithIssuer sets the expected issuer
func WithIssuer(issuer string) ValidatorOption {
	return func(v *JWTValidator) {
		v.issuer = issuer
	}
}

// WithAudience sets the expected audience
func WithAudience(audience string) ValidatorOption {
	return func(v *JWTValidator) {
		v.audience = audience
	}
}

// WithAlgorithm sets the expected signing algorithm (default: RS256)
func WithAlgorithm(alg string) ValidatorOption {
	return func(v *JWTValidator) {
		v.alg = alg
	}
}

// NewJWTValidator creates a new JWT validator with proper signature verification
func NewJWTValidator(provider *JWKSProvider, opts ...ValidatorOption) *JWTValidator {
	v := &JWTValidator{
		provider: provider,
		alg:      "RS256",
	}
	for _, opt := range opts {
		opt(v)
	}
	if v.alg == "" {
		v.alg = "RS256"
	}
	return v
}

// Issuer returns the configured issuer for this validator
func (v *JWTValidator) Issuer() string {
	return v.issuer
}

// JWKS returns the current JWKS from the provider
func (v *JWTValidator) JWKS(ctx context.Context) (*JWKSResponse, error) {
	return v.provider.GetJWKS(ctx)
}

// keyFuncFor returns a jwt.Keyfunc that resolves the signing key by kid,
// bound to the request context so a cancelled request does not keep fetching.
func (v *JWTValidator) keyFuncFor(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (interface{}, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("token header missing kid")
		}

		jwks, err := v.provider.GetJWKS(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get JWKS: %w", err)
		}

		key, found := findJWK(jwks, kid)
		if !found {
			// The issuer may have rotated its keys since the cache was filled.
			jwks, err = v.provider.Refresh(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to refresh JWKS: %w", err)
			}
			key, found = findJWK(jwks, kid)
		}
		if !found {
			return nil, fmt.Errorf("no matching JWKS key for kid: %s", kid)
		}

		return publicKeyFromJWK(key)
	}
}

// findJWK returns the key with the given kid.
func findJWK(jwks *JWKSResponse, kid string) (JWK, bool) {
	for _, key := range jwks.Keys {
		if key.Kid == kid {
			return key, true
		}
	}
	return JWK{}, false
}

// publicKeyFromJWK builds a verification key from a JWK, preferring the
// certificate chain when the issuer publishes one.
func publicKeyFromJWK(key JWK) (interface{}, error) {
	if key.Kty != "RSA" {
		return nil, fmt.Errorf("unsupported key type: %s", key.Kty)
	}

	if len(key.X5c) > 0 {
		cert, err := parseX509Cert(key.X5c[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse x5c certificate: %w", err)
		}
		return cert, nil
	}

	pubKey, err := decodeJWKToPublicKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWK: %w", err)
	}
	return pubKey, nil
}

// ValidateToken validates an OIDC access token with full signature verification.
// Signing method, expiry, issuer and audience are all enforced by the parser, so
// a token that reaches the return has already passed every check.
func (v *JWTValidator) ValidateToken(ctx context.Context, tokenString string) (*identity.JWTClaims, error) {
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{v.alg}),
		jwt.WithExpirationRequired(),
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, v.keyFuncFor(ctx), opts...)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	if claims.ExpiresAt == nil {
		return nil, fmt.Errorf("token has no expiry")
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("token has no subject")
	}

	return &identity.JWTClaims{
		Subject:           claims.Subject,
		Audience:          strings.Join(claims.Audience, ","),
		Expiry:            claims.ExpiresAt.Unix(),
		Issuer:            claims.Issuer,
		Email:             claims.Email,
		PreferredUsername: claims.PreferredUsername,
		Name:              claims.Name,
	}, nil
}
