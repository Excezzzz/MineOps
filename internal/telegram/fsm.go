package telegram

import "sync"

// FSM states (key — user_id).
const (
	fsmNone             = ""
	fsmOnbWaitingCookie = "onb:waiting_cookie"
	fsmOnbSelecting     = "onb:selecting"
	fsmAdminWaitCookie  = "admin:waiting_cookie"
)

type FSM struct {
	mu     sync.Mutex
	states map[int64]string
	data   map[int64]map[string]any
}

func NewFSM() *FSM {
	return &FSM{
		states: make(map[int64]string),
		data:   make(map[int64]map[string]any),
	}
}

func (f *FSM) Set(userID int64, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[userID] = state
	if state == fsmNone {
		delete(f.data, userID)
	}
}

func (f *FSM) Get(userID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[userID]
}

func (f *FSM) Clear(userID int64) {
	f.Set(userID, fsmNone)
}

func (f *FSM) SetData(userID int64, key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data[userID] == nil {
		f.data[userID] = make(map[string]any)
	}
	f.data[userID][key] = value
}

func (f *FSM) GetData(userID int64, key string) (any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.data[userID]
	if !ok {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}
