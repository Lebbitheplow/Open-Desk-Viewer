package httpx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/OpenDeskViewer/platform/api/internal/identity"
)

type contextKey string

const requestIDKey contextKey = "request_id"
const userKey contextKey = "user"

// newRequestID generates a unique request ID
func newRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

// withRequestID adds request ID to context
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// requestIDFromContext retrieves request ID from context
func requestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return "unknown"
}

// withUser adds user to context
func withUser(ctx context.Context, user *identity.User) context.Context {
	return context.WithValue(ctx, userKey, user)
}

// GetUserFromContext retrieves user from context and boolean indicating presence
func GetUserFromContext(ctx context.Context) (*identity.User, bool) {
	if user, ok := ctx.Value(userKey).(*identity.User); ok {
		return user, true
	}
	return nil, false
}

// GetUserIDFromContext retrieves user ID from context and boolean indicating presence
func GetUserIDFromContext(ctx context.Context) (int64, bool) {
	user, ok := GetUserFromContext(ctx)
	if ok {
		return user.ID, true
	}
	return 0, false
}

// debugStack returns the current stack trace
func debugStack() []byte {
	return debug.Stack()
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error"`
}

// ProblemResponse represents a RFC 7807 problem document
type ProblemResponse struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// WriteJSON writes a JSON response body
func WriteJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// WriteError writes an error response
func WriteError(w http.ResponseWriter, status int, err string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: err})
}

// WriteProblem writes a RFC 7807 problem response
func WriteProblem(w http.ResponseWriter, status int, problem ProblemResponse) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(problem)
}

// fmt is kept for compatibility with any remaining string formatting needs
var _ = fmt.Sprintf
