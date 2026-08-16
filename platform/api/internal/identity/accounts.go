package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AccountProvisioner creates and removes the identity-provider account behind a
// portal user.
//
// It exists because R1 asks an administrator to create and remove manager
// accounts, and until now a users row appeared only as a side effect of
// somebody signing in through Keycloak. The realm has registrationAllowed
// false, so "invite them and let them sign up" is not a path either: without
// this, creating an account means container-level access to the Keycloak admin
// console, which platform/Caddyfile deliberately does not route.
//
// It is an interface so the handler can be exercised without a Keycloak, and so
// a deployment that wants to keep account creation in the identity provider can
// simply leave it unconfigured: the route then answers 503 naming the settings
// it needs rather than half-creating a user.
type AccountProvisioner interface {
	// CreateAccount creates an enabled account that must change its password on
	// first sign-in, and returns the subject the token will carry.
	CreateAccount(ctx context.Context, email, displayName string) (Account, error)
	// DeleteAccount removes the account. An account that is already gone is not
	// an error: the local row is what the caller is really removing.
	DeleteAccount(ctx context.Context, subject string) error
}

// Account is what CreateAccount managed to create.
type Account struct {
	// Subject is the identity provider's own identifier, which is what
	// users.keycloak_subject stores and what a token's sub claim carries.
	Subject string
	// TemporaryPassword is returned exactly once, to the administrator who
	// created the account, and is never stored here. The account carries the
	// UPDATE_PASSWORD required action, so it is spent on first sign-in.
	TemporaryPassword string
}

// ErrAccountExists reports an email the identity provider already knows.
var ErrAccountExists = errors.New("an account with that email already exists")

// KeycloakAdmin is the AccountProvisioner backed by Keycloak's admin REST API.
//
// It authenticates as the odv-api service account rather than as a realm
// administrator: the client secret is one the API already holds, and the
// account needs only the realm-management roles manage-users and view-users.
// A master-realm admin credential in the API's configuration would be a much
// larger thing to hold for the two calls made here.
type KeycloakAdmin struct {
	baseURL      string
	realm        string
	clientID     string
	clientSecret string
	http         *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewKeycloakAdmin returns nil when the deployment has not configured the
// pieces this needs, which is what makes the feature optional rather than a
// startup failure.
func NewKeycloakAdmin(baseURL, realm, clientID, clientSecret string) *KeycloakAdmin {
	if baseURL == "" || realm == "" || clientID == "" || clientSecret == "" {
		return nil
	}
	return &KeycloakAdmin{
		baseURL:      strings.TrimSuffix(baseURL, "/"),
		realm:        realm,
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 15 * time.Second},
	}
}

// CreateAccount implements AccountProvisioner.
func (k *KeycloakAdmin) CreateAccount(ctx context.Context, email, displayName string) (Account, error) {
	token, err := k.accessToken(ctx)
	if err != nil {
		return Account{}, err
	}

	temporary, err := temporaryPassword()
	if err != nil {
		return Account{}, err
	}

	first, last := splitName(displayName)
	body, err := json.Marshal(map[string]any{
		"username":      email,
		"email":         email,
		"firstName":     first,
		"lastName":      last,
		"enabled":       true,
		"emailVerified": false,
		// The password is temporary in Keycloak's own sense: the account cannot
		// complete a sign-in without replacing it, so the value handed to the
		// administrator is not a lasting credential.
		"requiredActions": []string{"UPDATE_PASSWORD"},
		"credentials": []map[string]any{{
			"type":      "password",
			"value":     temporary,
			"temporary": true,
		}},
	})
	if err != nil {
		return Account{}, fmt.Errorf("failed to build the account request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.usersURL(), bytes.NewReader(body))
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.http.Do(req)
	if err != nil {
		return Account{}, fmt.Errorf("failed to reach the identity provider: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusConflict:
		return Account{}, ErrAccountExists
	default:
		return Account{}, fmt.Errorf("the identity provider refused to create the account: %s", describe(resp))
	}

	// Keycloak returns the new subject only in the Location header.
	location := resp.Header.Get("Location")
	subject := location[strings.LastIndex(location, "/")+1:]
	if subject == "" || strings.Contains(subject, "/") {
		return Account{}, fmt.Errorf("the identity provider created the account but returned no id (Location: %q)", location)
	}

	return Account{Subject: subject, TemporaryPassword: temporary}, nil
}

// DeleteAccount implements AccountProvisioner.
func (k *KeycloakAdmin) DeleteAccount(ctx context.Context, subject string) error {
	token, err := k.accessToken(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, k.usersURL()+"/"+url.PathEscape(subject), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach the identity provider: %w", err)
	}
	defer resp.Body.Close()

	// 404 means the account is already gone, which is the state the caller
	// asked for. Failing here would leave an administrator unable to remove a
	// local row because of a Keycloak account somebody else deleted first.
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("the identity provider refused to delete the account: %s", describe(resp))
}

func (k *KeycloakAdmin) usersURL() string {
	return k.baseURL + "/admin/realms/" + url.PathEscape(k.realm) + "/users"
}

// accessToken returns a cached client-credentials token, fetching a new one
// when the current one is close to expiring.
func (k *KeycloakAdmin) accessToken(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.token != "" && time.Now().Before(k.expires) {
		return k.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {k.clientID},
		"client_secret": {k.clientSecret},
	}
	endpoint := k.baseURL + "/realms/" + url.PathEscape(k.realm) + "/protocol/openid-connect/token"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach the identity provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the identity provider refused the service account: %s", describe(resp))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("the identity provider returned an unreadable token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("the identity provider returned an empty token")
	}

	k.token = payload.AccessToken
	// Expire early, so a token that is valid when checked is still valid when
	// the request that uses it arrives.
	k.expires = time.Now().Add(time.Duration(payload.ExpiresIn)*time.Second - 30*time.Second)

	return k.token, nil
}

// describe renders a failing response without leaking a whole HTML error page
// into the log.
func describe(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return resp.Status
	}
	return resp.Status + ": " + detail
}

// splitName maps a display name onto Keycloak's two name fields, which is what
// the token's name claim is built from.
func splitName(displayName string) (first, last string) {
	fields := strings.Fields(displayName)
	switch len(fields) {
	case 0:
		return "", ""
	case 1:
		return fields[0], ""
	default:
		return fields[0], strings.Join(fields[1:], " ")
	}
}

// temporaryPassword returns a value that is shown once and replaced on first
// sign-in. 24 bytes of CSPRNG output, for the same reason CreateSession uses
// 32: a password an administrator reads aloud is still a credential until it is
// changed.
func temporaryPassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("failed to generate a temporary password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
