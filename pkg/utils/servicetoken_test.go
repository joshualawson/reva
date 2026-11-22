package utils

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
)

// fakeGateway counts Authenticate calls and answers with whatever the test queued.
type fakeGateway struct {
	gateway.GatewayAPIClient
	calls atomic.Int32
	mu    sync.Mutex
	next  func(req *gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error)
}

func (f *fakeGateway) Authenticate(_ context.Context, req *gateway.AuthenticateRequest, _ ...grpc.CallOption) (*gateway.AuthenticateResponse, error) {
	f.calls.Add(1)
	f.mu.Lock()
	next := f.next
	f.mu.Unlock()
	return next(req)
}

func (f *fakeGateway) respond(fn func(req *gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = fn
}

func okToken(token string) func(*gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
	return func(*gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
		return &gateway.AuthenticateResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}, Token: token}, nil
	}
}

func signedToken(t *testing.T, issued, expires time.Time) string {
	t.Helper()
	tkn, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(issued),
		ExpiresAt: jwt.NewNumericDate(expires),
	}).SignedString([]byte("test-only"))
	if err != nil {
		t.Fatal(err)
	}
	return tkn
}

func withTestCache(t *testing.T, now *time.Time) {
	t.Helper()
	previous := serviceTokens
	serviceTokens = newServiceTokenCache()
	serviceTokens.now = func() time.Time { return *now }
	t.Cleanup(func() { serviceTokens = previous })
}

func TestServiceTokenBurstAuthenticatesOnce(t *testing.T) {
	now := time.Now()
	withTestCache(t, &now)
	gw := &fakeGateway{}
	gw.respond(func(req *gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
		time.Sleep(20 * time.Millisecond)
		return okToken(signedToken(t, now, now.Add(time.Hour)))(req)
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := GetServiceUserToken(context.Background(), gw, "svc", "secret")
			if err != nil || token == "" {
				t.Errorf("unexpected result: %q, %v", token, err)
			}
		}()
	}
	wg.Wait()

	if got := gw.calls.Load(); got != 1 {
		t.Fatalf("expected one authentication for a burst, got %d", got)
	}
}

func TestServiceTokenRefreshesBeforeExpiry(t *testing.T) {
	now := time.Now()
	withTestCache(t, &now)
	gw := &fakeGateway{}
	gw.respond(okToken(signedToken(t, now, now.Add(10*time.Minute))))

	first, err := GetServiceUserToken(context.Background(), gw, "svc", "secret")
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(7 * time.Minute)
	if again, _ := GetServiceUserToken(context.Background(), gw, "svc", "secret"); again != first || gw.calls.Load() != 1 {
		t.Fatalf("token should still be reused at 70%% of its lifetime (calls=%d)", gw.calls.Load())
	}

	// 80% of a ten-minute lifetime is eight minutes, so this crosses the refresh point.
	now = now.Add(2 * time.Minute)
	gw.respond(okToken(signedToken(t, now, now.Add(10*time.Minute))))
	second, err := GetServiceUserToken(context.Background(), gw, "svc", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if second == first || gw.calls.Load() != 2 {
		t.Fatalf("expected a fresh token after the refresh point (calls=%d)", gw.calls.Load())
	}
}

func TestServiceTokenOpaqueFallsBackToDefaultTTL(t *testing.T) {
	now := time.Now()
	withTestCache(t, &now)
	gw := &fakeGateway{}
	gw.respond(okToken("opaque"))

	if _, err := GetServiceUserToken(context.Background(), gw, "svc", "secret"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(DefaultServiceTokenTTL - time.Second)
	if _, err := GetServiceUserToken(context.Background(), gw, "svc", "secret"); err != nil || gw.calls.Load() != 1 {
		t.Fatalf("opaque token should be reused inside the default TTL (calls=%d, err=%v)", gw.calls.Load(), err)
	}
	now = now.Add(2 * time.Second)
	if _, err := GetServiceUserToken(context.Background(), gw, "svc", "secret"); err != nil || gw.calls.Load() != 2 {
		t.Fatalf("opaque token should be refreshed after the default TTL (calls=%d, err=%v)", gw.calls.Load(), err)
	}
}

func TestServiceTokenErrorDoesNotPoisonCache(t *testing.T) {
	now := time.Now()
	withTestCache(t, &now)
	gw := &fakeGateway{}
	gw.respond(func(*gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
		return nil, errors.New("gateway down")
	})

	if _, err := GetServiceUserToken(context.Background(), gw, "svc", "secret"); err == nil {
		t.Fatal("expected the transport error to be returned")
	}
	gw.respond(func(*gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
		return &gateway.AuthenticateResponse{Status: &rpc.Status{Code: rpc.Code_CODE_UNAUTHENTICATED}}, nil
	})
	if _, err := GetServiceUserToken(context.Background(), gw, "svc", "secret"); !IsStatusCodeError(err, rpc.Code_CODE_UNAUTHENTICATED) {
		t.Fatalf("expected the status error to be returned, got %v", err)
	}

	gw.respond(okToken(signedToken(t, now, now.Add(time.Hour))))
	token, err := GetServiceUserToken(context.Background(), gw, "svc", "secret")
	if err != nil || token == "" {
		t.Fatalf("expected a token once the gateway recovers, got %q, %v", token, err)
	}
	if _, err := GetServiceUserToken(context.Background(), gw, "svc", "secret"); err != nil || gw.calls.Load() != 3 {
		t.Fatalf("expected the recovered token to be cached (calls=%d, err=%v)", gw.calls.Load(), err)
	}
}

func TestServiceTokenKeyedPerAccountAndSecret(t *testing.T) {
	now := time.Now()
	withTestCache(t, &now)
	gw := &fakeGateway{}
	gw.respond(func(req *gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
		return okToken(req.ClientId + ":" + req.ClientSecret)(req)
	})

	a, _ := GetServiceUserToken(context.Background(), gw, "a", "1")
	b, _ := GetServiceUserToken(context.Background(), gw, "b", "1")
	a2, _ := GetServiceUserToken(context.Background(), gw, "a", "2")
	a1, _ := GetServiceUserToken(context.Background(), gw, "a", "1")
	if a != "a:1" || b != "b:1" || a2 != "a:2" || a1 != a || gw.calls.Load() != 3 {
		t.Fatalf("cache must be keyed by account and secret (calls=%d)", gw.calls.Load())
	}
}

func TestServiceTokenRefreshAt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// Short tokens refresh at 80% of their lifetime, long ones a minute before expiry.
	if got, want := serviceTokenRefreshAt(signedToken(t, now, now.Add(10*time.Minute)), now), now.Add(8*time.Minute); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, want := serviceTokenRefreshAt(signedToken(t, now, now.Add(2*time.Minute)), now), now.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, want := serviceTokenRefreshAt("not-a-jwt", now), now.Add(DefaultServiceTokenTTL); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
