// Реестр per-owner менеджеров: общий HTTP-клиент и кеш сессий
// на всё время жизни процесса.
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

	mu      sync.Mutex
	byOwner map[int64]*Manager
}

// NewRegistry создаёт общий реестр.
func NewRegistry(db *database.DB, httpClient *http.Client) *Registry {
	return &Registry{
		db:         db,
		httpClient: httpClient,
		byOwner:    make(map[int64]*Manager),
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