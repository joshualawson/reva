package utils

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

// DefaultServiceTokenTTL bounds how long an opaque service token without a readable expiry is reused.
var DefaultServiceTokenTTL = 5 * time.Minute

// serviceTokenRefreshMargin is how long before expiry a cached token is refreshed.
const serviceTokenRefreshMargin = time.Minute

// serviceTokenRefreshFraction is the share of a token's lifetime after which it is refreshed.
const serviceTokenRefreshFraction = 0.8

// serviceTokens is process-wide so every event consumer in a service shares one token per account.
var serviceTokens = newServiceTokenCache()

type serviceTokenKey struct {
	id     string
	secret [sha256.Size]byte
}

type serviceTokenEntry struct {
	token     string
	refreshAt time.Time
}

// serviceTokenCache reuses service-account tokens so consumers stop authenticating per event.
type serviceTokenCache struct {
	mu      sync.Mutex
	entries map[serviceTokenKey]serviceTokenEntry
	group   singleflight.Group
	now     func() time.Time
}

func newServiceTokenCache() *serviceTokenCache {
	return &serviceTokenCache{entries: map[serviceTokenKey]serviceTokenEntry{}, now: time.Now}
}

func (c *serviceTokenCache) get(ctx context.Context, gwc gateway.GatewayAPIClient, id, secret string) (string, error) {
	key := serviceTokenKey{id: id, secret: sha256.Sum256([]byte(secret))}
	if token, ok := c.lookup(key); ok {
		return token, nil
	}

	// Collapse a burst of misses into one gateway round trip.
	token, err, _ := c.group.Do(id+"\x00"+hex.EncodeToString(key.secret[:]), func() (interface{}, error) {
		if token, ok := c.lookup(key); ok {
			return token, nil
		}
		token, err := authenticateServiceUser(ctx, gwc, id, secret)
		if err != nil {
			c.drop(key)
			return "", err
		}
		c.store(key, token)
		return token, nil
	})
	if err != nil {
		return "", err
	}
	return token.(string), nil
}

func (c *serviceTokenCache) lookup(key serviceTokenKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.refreshAt) {
		return "", false
	}
	return entry.token, true
}

func (c *serviceTokenCache) store(key serviceTokenKey, token string) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = serviceTokenEntry{token: token, refreshAt: serviceTokenRefreshAt(token, now)}
}

func (c *serviceTokenCache) drop(key serviceTokenKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *serviceTokenCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[serviceTokenKey]serviceTokenEntry{}
}

// serviceTokenRefreshAt reads the unverified exp claim because only the gateway holds the signing secret.
func serviceTokenRefreshAt(token string, now time.Time) time.Time {
	claims := &jwt.RegisteredClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil || claims.ExpiresAt == nil {
		return now.Add(DefaultServiceTokenTTL)
	}
	exp := claims.ExpiresAt.Time
	issued := now
	if claims.IssuedAt != nil && claims.IssuedAt.Time.Before(now) {
		issued = claims.IssuedAt.Time
	}
	early := issued.Add(time.Duration(float64(exp.Sub(issued)) * serviceTokenRefreshFraction))
	if margin := exp.Add(-serviceTokenRefreshMargin); margin.Before(early) {
		return margin
	}
	return early
}

func authenticateServiceUser(ctx context.Context, gwc gateway.GatewayAPIClient, id, secret string) (string, error) {
	authRes, err := gwc.Authenticate(ctx, &gateway.AuthenticateRequest{
		Type:         "serviceaccounts",
		ClientId:     id,
		ClientSecret: secret,
	})
	if err != nil {
		return "", err
	}

	if err := checkStatusCode("authenticating service user", authRes.GetStatus().GetMessage(), authRes.GetStatus().GetCode()); err != nil {
		return "", err
	}

	return authRes.Token, nil
}
