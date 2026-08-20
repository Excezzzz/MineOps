// Реестр per-owner менеджеров: общий HTTP-клиент (с перехватчиком ошибок
// аутентификации) и кеш сессий на всё время жизни процесса.
package aternos

import (
	"net/http"
	"sync"

	"mineops/internal/database"
)

// Registry — фабрика менеджеров по владельцам.
type Registry struct {
	db         *database.DB
	httpClient *http.Client

	mu       sync.Mutex
	byOwner  map[int64]*Manager
	authHook AuthFailureHook
}

// NewRegistry создаёт общий реестр. HTTP-клиент оборачивается перехватчиком
// ошибок аутентификации Aternos.
func NewRegistry(db *database.DB, httpClient *http.Client) *Registry {
	r := &Registry{
		db:      db,
		byOwner: make(map[int64]*Manager),
	}
	r.httpClient = &http.Client{
		Timeout: httpClient.Timeout,
		Transport: &authInterceptor{
			base:   effectiveTransport(httpClient.Transport),
			notify: func(ownerID int64) { r.notifyAuth(ownerID) },
		},
	}
	return r
}

// SetAuthHook задаёт перехватчик ошибок аутентификации (вызывается с id
// владельца; потокобезопасно, может быть заменён после старта бота).
func (r *Registry) SetAuthHook(hook AuthFailureHook) {
	r.mu.Lock()
	r.authHook = hook
	r.mu.Unlock()
}

func (r *Registry) notifyAuth(ownerID int64) {
	r.mu.Lock()
	hook := r.authHook
	r.mu.Unlock()
	if hook != nil {
		hook(ownerID)
	}
}

// For возвращает (и при необходимости создаёт) менеджера владельца.
func (r *Registry) For(ownerID int64) *Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byOwner[ownerID]
	if !ok {
		m = NewManager(ownerID, r.db, r.httpClient)
		r.byOwner[ownerID] = m
	}
	return m
}
