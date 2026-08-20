package aternos

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"mineops/internal/crypto"
	"mineops/internal/database"
	"mineops/internal/i18n"
)

type Manager struct {
	ownerID int64
	db      *database.DB

	httpClient *http.Client

	mu        sync.Mutex
	cache     map[int64]*Session // owner_id -> session
	failUntil map[int64]time.Time
	locks     map[int64]*sync.Mutex
}

const (
	clientTTL  = 30 * time.Minute // session cache TTL
	cfCooldown = 5 * time.Minute  // pause after Cloudflare ban
)

// (shared http.Client)
func NewManager(ownerID int64, db *database.DB, httpClient *http.Client) *Manager {
	return &Manager{
		ownerID:    ownerID,
		db:         db,
		httpClient: httpClient,
		cache:      make(map[int64]*Session),
		failUntil:  make(map[int64]time.Time),
		locks:      make(map[int64]*sync.Mutex),
	}
}

func (m *Manager) lockFor(ownerID int64) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.locks[ownerID]
	if !ok {
		mu = &sync.Mutex{}
		m.locks[ownerID] = mu
	}
	return mu
}

func (m *Manager) lang() string {
	if o, _ := m.db.GetOwner(m.ownerID); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

// Matching is done against known message constants (equality or containment); unknown ones pass through as-is.
func (m *Manager) localize(err error) error {
	if err == nil {
		return nil
	}
	lang := m.lang()
	var aerr *Error
	if !asError(err, &aerr) {
		return err
	}
	msg := aerr.Message
	for _, pair := range [][2]string{
		{msgCloudflare, "err_cloudflare"},
		{msgSessionExpired, "err_session_expired"},
		{msgOwnerNotFound, "err_owner_not_found"},
		{msgSessionNotSet, "err_session_not_set"},
		{msgServerNotFound, "err_server_not_found"},
	} {
		if msg == pair[0] || strings.Contains(msg, pair[0]) {
			return NewError(i18n.T(lang, pair[1]))
		}
	}
	return err
}

func (m *Manager) cookie() (string, error) {
	owner, err := m.db.GetOwner(m.ownerID)
	if err != nil {
		return "", NewError(i18n.T(m.lang(), "err_profile_read", err.Error()))
	}
	if owner == nil {
		return "", NewError(msgOwnerNotFound)
	}
	cookie := crypto.DecryptSession(owner.AternosSession)
	if cookie == "" {
		return "", NewError(msgSessionNotSet)
	}
	return cookie, nil
}

func (m *Manager) getSession(ctx context.Context, ownerID int64, cookie string) (*Session, error) {
	m.mu.Lock()
	cached := m.cache[ownerID]
	blocked := time.Now().Before(m.failUntil[ownerID])
	m.mu.Unlock()

	if cached != nil && time.Now().Before(cached.expiresAt) {
		return cached, nil
	}
	if blocked {
		return nil, NewError(msgCloudflare)
	}

	session, err := newSession(ctx, m.httpClient, cookie)
	if err != nil {
		var aerr *Error
		if ok := asError(err, &aerr); ok && isCloudflare(aerr) {
			m.mu.Lock()
			m.failUntil[ownerID] = time.Now().Add(cfCooldown)
			m.mu.Unlock()
		}
		return nil, err
	}
	m.mu.Lock()
	m.cache[ownerID] = session
	delete(m.failUntil, ownerID)
	m.mu.Unlock()
	return session, nil
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func (m *Manager) run(ctx context.Context, fn func(ctx context.Context) error) error {
	mu := m.lockFor(m.ownerID)
	mu.Lock()
	defer mu.Unlock()

	cookie, err := m.cookie()
	if err != nil {
		return err
	}
	ctx = ctxWithOwner(ctx, m.ownerID)
	if err := fn(ctxWithCookie(ctx, cookie)); err != nil {
		var aerr *Error
		if asError(err, &aerr) {
			return m.localize(err)
		}
		return m.localize(NewError(fmt.Sprintf("%s: %v",
			i18n.T(m.lang(), "err_request_failed"), err)))
	}
	return nil
}

type ctxKey int

const cookieKey ctxKey = 1

func ctxWithCookie(ctx context.Context, cookie string) context.Context {
	return context.WithValue(ctx, cookieKey, cookie)
}

func cookieFrom(ctx context.Context) string {
	if v, ok := ctx.Value(cookieKey).(string); ok {
		return v
	}
	return ""
}

// (no network requests while the cache is fresh)
func (m *Manager) CheckSession(ctx context.Context) error {
	return m.run(ctx, func(ctx context.Context) error {
		_, err := m.getSession(ctx, m.ownerID, cookieFrom(ctx))
		return err
	})
}

func (m *Manager) ListAccountServers(ctx context.Context) ([]ServerBrief, error) {
	var out []ServerBrief
	err := m.run(ctx, func(ctx context.Context) error {
		session, err := m.getSession(ctx, m.ownerID, cookieFrom(ctx))
		if err != nil {
			return err
		}
		out, err = session.ListServers(ctx)
		return err
	})
	return out, err
}

func (m *Manager) StartServer(ctx context.Context, serverID int64) (string, error) {
	server, err := m.db.GetServer(serverID)
	if err != nil {
		return "", err
	}
	if server == nil || server.OwnerID != m.ownerID {
		return "", NewError(msgServerNotFound)
	}
	err = m.run(ctx, func(ctx context.Context) error {
		session, err := m.getSession(ctx, m.ownerID, cookieFrom(ctx))
		if err != nil {
			return err
		}
		return session.Start(ctx, server.AternosID)
	})
	if err != nil {
		return "", err
	}
	_ = m.db.LogAction(m.ownerID, "server_start", server.DisplayName, 0, 0, serverID)
	return i18n.T(m.lang(), "srv_start_requested", html.EscapeString(server.DisplayName)), nil
}

func (m *Manager) StopServer(ctx context.Context, serverID int64) (string, error) {
	server, err := m.db.GetServer(serverID)
	if err != nil {
		return "", err
	}
	if server == nil || server.OwnerID != m.ownerID {
		return "", NewError(msgServerNotFound)
	}
	err = m.run(ctx, func(ctx context.Context) error {
		session, err := m.getSession(ctx, m.ownerID, cookieFrom(ctx))
		if err != nil {
			return err
		}
		return session.Stop(ctx, server.AternosID)
	})
	if err != nil {
		return "", err
	}
	_ = m.db.LogAction(m.ownerID, "server_stop", server.DisplayName, 0, 0, serverID)
	return fmt.Sprintf("Server %s stop requested.", html.EscapeString(server.DisplayName)), nil
}

func (m *Manager) ConfirmServer(ctx context.Context, serverID int64) error {
	server, err := m.db.GetServer(serverID)
	if err != nil {
		return err
	}
	if server == nil || server.OwnerID != m.ownerID {
		return NewError(msgServerNotFound)
	}
	err = m.run(ctx, func(ctx context.Context) error {
		session, err := m.getSession(ctx, m.ownerID, cookieFrom(ctx))
		if err != nil {
			return err
		}
		return session.Confirm(ctx, server.AternosID)
	})
	if err != nil {
		return err
	}
	_ = m.db.LogAction(m.ownerID, "server_confirm", server.DisplayName, 0, 0, serverID)
	return nil
}

// (for the watcher and the panel)
func (m *Manager) FetchInfo(ctx context.Context, serverID int64) (*ServerInfo, error) {
	server, err := m.db.GetServer(serverID)
	if err != nil {
		return nil, err
	}
	if server == nil || server.OwnerID != m.ownerID {
		return nil, NewError(msgServerNotFound)
	}
	var out *ServerInfo
	err = m.run(ctx, func(ctx context.Context) error {
		session, err := m.getSession(ctx, m.ownerID, cookieFrom(ctx))
		if err != nil {
			return err
		}
		out, err = session.FetchServerInfo(ctx, server.AternosID)
		return err
	})
	return out, err
}

func (m *Manager) ProbeCookie(ctx context.Context, cookie string) error {
	mu := m.lockFor(m.ownerID)
	mu.Lock()
	defer mu.Unlock()
	_, err := newSession(ctxWithOwner(ctx, m.ownerID), m.httpClient, cookie)
	if err != nil {
		var aerr *Error
		if asError(err, &aerr) && isCloudflare(aerr) {
			m.mu.Lock()
			m.failUntil[m.ownerID] = time.Now().Add(cfCooldown)
			m.mu.Unlock()
		}
		return err
	}
	return nil
}

func (m *Manager) ProbeSession(ctx context.Context, cookie string) ([]ServerBrief, error) {
	mu := m.lockFor(m.ownerID)
	mu.Lock()
	defer mu.Unlock()
	session, err := newSession(ctxWithOwner(ctx, m.ownerID), m.httpClient, cookie)
	if err != nil {
		var aerr *Error
		if asError(err, &aerr) && isCloudflare(aerr) {
			m.mu.Lock()
			m.failUntil[m.ownerID] = time.Now().Add(cfCooldown)
			m.mu.Unlock()
		}
		return nil, err
	}
	servers, err := session.ListServers(ctx)
	if err != nil {
		if isExpiredRedirect(err) {
			return nil, NewError(msgSessionExpired)
		}
		var aerr *Error
		if asError(err, &aerr) {
			return nil, err
		}
		return nil, NewError(fmt.Sprintf("failed to list servers: %v", err))
	}
	return servers, nil
}

func isExpiredRedirect(err error) bool {
	var aerr *Error
	return asError(err, &aerr) && aerr.Message == msgSessionExpired
}

func (m *Manager) UpdateSession(ctx context.Context, newCookie string) error {
	cookie := newCookie
	if cookie == "" {
		return NewError("cookie must not be empty")
	}
	if err := m.ProbeCookie(ctx, cookie); err != nil {
		return err
	}
	if err := m.db.UpdateOwnerSession(m.ownerID, crypto.EncryptSession(cookie)); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.cache, m.ownerID)
	delete(m.failUntil, m.ownerID)
	m.mu.Unlock()
	_ = m.db.LogAction(m.ownerID, "session_update", "кука обновлена", 0, 0, 0)
	return nil
}
