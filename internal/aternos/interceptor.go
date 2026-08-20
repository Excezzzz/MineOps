package aternos

import (
	"context"
	"net/http"
	"strings"
)

type AuthFailureHook func(ownerID int64)

type ownerCtxKey int

const ownerKey ownerCtxKey = 1

func ctxWithOwner(ctx context.Context, ownerID int64) context.Context {
	return context.WithValue(ctx, ownerKey, ownerID)
}

func ownerFrom(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ownerKey).(int64)
	return v, ok
}

type authInterceptor struct {
	base   http.RoundTripper
	notify AuthFailureHook
}

func (t *authInterceptor) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && isAuthFailure(resp) {
		if ownerID, ok := ownerFrom(req.Context()); ok && t.notify != nil {
			t.notify(ownerID)
		}
	}
	return resp, err
}

// isAuthFailure — признаки ошибки аутентификации Aternos:
// Cloudflare-блокировка (403), 401, 403 или редирект на /go/ (истекла кука).
func isAuthFailure(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return true
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/go/") {
			return true
		}
	}
	return false
}

func effectiveTransport(t http.RoundTripper) http.RoundTripper {
	if t != nil {
		return t
	}
	return http.DefaultTransport.(*http.Transport).Clone()
}
