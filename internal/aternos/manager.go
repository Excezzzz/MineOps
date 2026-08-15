// Менеджер Aternos: per-owner кеш сессий, сериализация запросов,
// cooldown при Cloudflare-бане (порт AternosManager из aternos_api.py).
package aternos

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"sync"
	"time"

	"mineops/internal/crypto"
	"mineops/internal/database"
)

// Manager — фасад работы с Aternos для одного владельца.
type Manager struct {
	ownerID int64
	db      *database.DB

	httpClient *http.Client

	mu        sync.Mutex
	cache     map[int64]*Session // owner_id -> сессия
	failUntil map[int64]time.Time
	locks     map[int64]*sync.Mutex
}

const (
	clientTTL    = 30 * time.Minute // TTL кеша сессий
	cfCooldown   = 5 * time.Minute  // пауза после Cloudflare-бана
)

// NewManager создаёт менеджер для владельца (общий http.Client разделяется).
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

// cookie возвращает расшифрованную куку владельца.
func (m *Manager) cookie() (string, error) {
	owner, err := m.db.GetOwner(m.ownerID)
	if err != nil {
		return "", NewError(fmt.Sprintf("Не удалось прочитать профиль: %v", err))
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

// getSession возвращает закешированную сессию или логинится заново.
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
	if err := fn(ctxWithCookie(ctx, cookie)); err != nil {
		var aerr *Error
		if asError(err, &aerr) {
			return err
		}
		return NewError(fmt.Sprintf("Не удалось выполнить запрос к Aternos: %v", err))
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

// CheckSession проверяет куку владельца из БД (без сетевых запросов, пока кеш свежий).
func (m *Manager) CheckSession(ctx context.Context) error {
	return m.run(ctx, func(ctx context.Context) error {
		_, err := m.getSession(ctx, m.ownerID, cookieFrom(ctx))
		return err
	})
}

// ListAccountServers возвращает серверы аккаунта владельца (для панели).
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

// StartServer запускает сервер владельца по его id в БД.
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
	return fmt.Sprintf("Запуск сервера %s запрошен.", html.EscapeString(server.DisplayName)), nil
}

// StopServer останавливает сервер владельца.
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
	return fmt.Sprintf("Остановка сервера %s запрошена.", html.EscapeString(server.DisplayName)), nil
}

// ConfirmServer подтверждает запуск сервера из очереди.
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

// FetchInfo возвращает `lastStatus` сервера (для queue_watcher и панели).
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

// ProbeCookie проверяет куку (вход + токен), не сохраняя её.
func (m *Manager) ProbeCookie(ctx context.Context, cookie string) error {
	mu := m.lockFor(m.ownerID)
	mu.Lock()
	defer mu.Unlock()
	_, err := newSession(ctx, m.httpClient, cookie)
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

// ProbeSession проверяет куку и возвращает серверы аккаунта (онбординг).
func (m *Manager) ProbeSession(ctx context.Context, cookie string) ([]ServerBrief, error) {
	mu := m.lockFor(m.ownerID)
	mu.Lock()
	defer mu.Unlock()
	session, err := newSession(ctx, m.httpClient, cookie)
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
		return nil, NewError(fmt.Sprintf("Не удалось получить список серверов: %v", err))
	}
	return servers, nil
}

func isExpiredRedirect(err error) bool {
	var aerr *Error
	return asError(err, &aerr) && aerr.Message == msgSessionExpired
}

// UpdateSession обновляет куку владельца: проверяет, шифрует, сохраняет, чистит кеш.
func (m *Manager) UpdateSession(ctx context.Context, newCookie string) error {
	cookie := newCookie
	if cookie == "" {
		return NewError("Кука не может быть пустой.")
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