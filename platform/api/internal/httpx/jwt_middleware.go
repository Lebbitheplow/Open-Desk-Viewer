package httpx

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/rs/zerolog/log"
)

const tokenKey contextKey = "auth_token"

// TokenValidator verifies a bearer token and returns its claims.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (*identity.JWTClaims, error)
}

// UserDirectory maps a credential onto a local user record: token claims for an
// OIDC bearer token, provisioning one on first sign-in, or the opaque session
// token that /api/login hands to the RustDesk client.
type UserDirectory interface {
	ResolveUser(ctx context.Context, claims *identity.JWTClaims) (*identity.User, error)
	GetSessionUser(ctx context.Context, sessionToken string) (*identity.User, error)
}

// withAuthToken stores the raw auth token in context
func withAuthToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey, token)
}

// GetAuthTokenFromContext retrieves the raw auth token from context
func GetAuthTokenFromContext(ctx context.Context) (string, bool) {
	if token, ok := ctx.Value(tokenKey).(string); ok {
		return token, true
	}
	return "", false
}

// JWTMiddleware validates JWT tokens from the Authorization header. Apply it to
// the protected route group only; public routes are registered on the base mux.
//
// Both dependencies are mandatory. A nil validator would let every request
// through unauthenticated, so it is rejected at construction rather than
// silently skipped per request.
func JWTMiddleware(validator TokenValidator, users UserDirectory) Middleware {
	if validator == nil {
		panic("httpx: JWTMiddleware requires a token validator")
	}
	if users == nil {
		panic("httpx: JWTMiddleware requires a user directory")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				WriteError(w, http.StatusUnauthorized, "missing authorization header")
				return
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				WriteError(w, http.StatusUnauthorized, "invalid authorization header format")
				return
			}

			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			ctx := withAuthToken(r.Context(), tokenString)
			requestID := requestIDFromContext(ctx)

			user, err := resolveCredential(ctx, validator, users, tokenString)
			if err != nil {
				log.Debug().Err(err).Str("request_id", requestID).Msg("credential rejected")
				WriteError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			if !user.Active {
				log.Info().Str("request_id", requestID).
					Int64("user_id", user.ID).
					Msg("rejected request from disabled account")
				WriteError(w, http.StatusUnauthorized, "account disabled")
				return
			}

			next.ServeHTTP(w, r.WithContext(withUser(ctx, user)))
		})
	}
}

// resolveCredential accepts the two credentials the platform issues: an OIDC
// bearer token from Keycloak, used by the portal, and the opaque session token
// that /api/login returns to the RustDesk client, which the client replays on
// every later call. A JWT is three dot-separated segments; anything else is
// treated as a session token, so a malformed JWT is never retried as one.
func resolveCredential(ctx context.Context, validator TokenValidator, users UserDirectory, token string) (*identity.User, error) {
	if strings.Count(token, ".") != 2 {
		user, err := users.GetSessionUser(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("no live session for the presented token: %w", err)
		}
		return user, nil
	}

	claims, err := validator.ValidateToken(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	user, err := users.ResolveUser(ctx, claims)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve user for subject %q: %w", claims.Subject, err)
	}
	return user, nil
}
