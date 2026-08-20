// Package database — SQLite storage with auto-migrations.
package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 5

// Owner — владелец (свой Aternos-аккаунт).
type Owner struct {
	UserID         int64
	Username       string
	FullName       string
	AternosSession string
	SessionValid   bool
	MaxServers     int
	LockdownMode   bool
	CreatedAt      string
	UpdatedAt      string
	PMPinnedMsgID  sql.NullInt64
	ScheduleTime   string
	ScheduleOnce   bool
	Lang           string
}

// Server — сервер Aternos владельца.
type Server struct {
	ID          int64
	OwnerID     int64
	AternosID   string
	ServerIP    string
	DisplayName string
	IsActive    bool
	AutoBackupH int
	AutoConfirm bool
	CreatedAt   string
}

// Chat — привязанный к владельцу групповой чат.
type Chat struct {
	ChatID      int64
	OwnerID     int64
	Title       string
	PinnedMsgID sql.NullInt64
	IsActive    bool
	CreatedAt   string
}

// ChatUser — участник чата и его права.
type ChatUser struct {
	UserID    int64
	ChatID    int64
	Username  string
	FullName  string
	HasAccess bool
	CreatedAt string
}

// AuditEntry — запись журнала действий владельца.
type AuditEntry struct {
	ID        int64
	OwnerID   int64
	UserID    sql.NullInt64
	ChatID    sql.NullInt64
	ServerID  sql.NullInt64
	Action    string
	Details   string
	CreatedAt string
}

// DB — обёртка над *sql.DB с миграциями и CRUD.
type DB struct {
	path string
	db   *sql.DB
}

func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05")
}

// Open открывает (и при необходимости создаёт) БД, приводит схему к актуальной.
func Open(dbPath string) (*DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	d := &DB{path: dbPath, db: conn}

	if _, err := d.db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return nil, err
	}
	if _, err := d.db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return nil, err
	}
	if _, err := d.db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, err
	}
	if err := d.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

// Close закрывает соединение.
func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) tableNames() (map[string]bool, error) {
	rows, err := d.db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = true
	}
	return names, rows.Err()
}

func (d *DB) tableColumns(table string) (map[string]bool, error) {
	rows, err := d.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func (d *DB) currentVersion() (int, error) {
	names, err := d.tableNames()
	if err != nil {
		return 0, err
	}
	if !names["db_version"] {
		return 0, nil
	}
	var v int
	err = d.db.QueryRow("SELECT MAX(version) FROM db_version").Scan(&v)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

func (d *DB) setVersion(v int) error {
	_, err := d.db.Exec("INSERT INTO db_version (version, applied_at) VALUES (?, ?)", v, nowISO())
	return err
}

func (d *DB) migrate() error {
	version, err := d.currentVersion()
	if err != nil {
		return err
	}

	if version == 0 {
		if _, err := d.db.Exec("CREATE TABLE IF NOT EXISTS db_version (" +
			"version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')"); err != nil {
			return err
		}
		if err := d.setVersion(1); err != nil {
			return err
		}
		version = 1
	}

	if version < 2 {
		if err := d.createV2Schema(); err != nil {
			return err
		}
		if err := d.setVersion(2); err != nil {
			return err
		}
		version = 2
	}

	if version < 3 {
		_, err := d.db.Exec(`
			CREATE TABLE IF NOT EXISTS server_meta (
				server_id  INTEGER PRIMARY KEY
				           REFERENCES servers(id) ON DELETE CASCADE,
				mc_port    INTEGER,
				updated_at TEXT NOT NULL DEFAULT ''
			);`)
		if err != nil {
			return err
		}
		if err := d.setVersion(3); err != nil {
			return err
		}
		version = 3
	}

	if version < 4 {
		cols, err := d.tableColumns("owners")
		if err != nil {
			return err
		}
		if !cols["pm_pinned_msg_id"] {
			if _, err := d.db.Exec("ALTER TABLE owners ADD COLUMN pm_pinned_msg_id INTEGER"); err != nil {
				return err
			}
		}
		if err := d.setVersion(4); err != nil {
			return err
		}
		version = 4
	}

	if version < 5 {
		cols, err := d.tableColumns("owners")
		if err != nil {
			return err
		}
		if !cols["schedule_time"] {
			if _, err := d.db.Exec("ALTER TABLE owners ADD COLUMN schedule_time TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
		if !cols["schedule_once"] {
			if _, err := d.db.Exec("ALTER TABLE owners ADD COLUMN schedule_once INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
		if !cols["lang"] {
			if _, err := d.db.Exec("ALTER TABLE owners ADD COLUMN lang TEXT NOT NULL DEFAULT 'ru'"); err != nil {
				return err
			}
		}
		if err := d.setVersion(5); err != nil {
			return err
		}
		version = 5
	}

	if version < SchemaVersion {
		return fmt.Errorf("неизвестная версия схемы БД: %d", version)
	}
	return nil
}

func (d *DB) createV2Schema() error {
	names, err := d.tableNames()
	if err != nil {
		return err
	}
	legacy := map[string]string{
		"chats":    "chats_legacy",
		"users":    "users_legacy",
		"settings": "settings_legacy",
	}
	for old, new := range legacy {
		if names[old] && !names[new] {
			if _, err := d.db.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s", old, new)); err != nil {
				return err
			}
		}
	}
	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS owners (
			user_id         INTEGER PRIMARY KEY,
			username        TEXT NOT NULL DEFAULT '',
			full_name       TEXT NOT NULL DEFAULT '',
			aternos_session TEXT NOT NULL DEFAULT '',
			session_valid   INTEGER NOT NULL DEFAULT 1,
			max_servers     INTEGER NOT NULL DEFAULT 2,
			lockdown_mode   INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL DEFAULT '',
			updated_at      TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS servers (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_id      INTEGER NOT NULL REFERENCES owners(user_id) ON DELETE CASCADE,
			aternos_id    TEXT NOT NULL DEFAULT '',
			server_ip     TEXT NOT NULL DEFAULT '',
			display_name  TEXT NOT NULL DEFAULT '',
			is_active     INTEGER NOT NULL DEFAULT 1,
			auto_backup_h INTEGER NOT NULL DEFAULT 0,
			auto_confirm  INTEGER NOT NULL DEFAULT 1,
			created_at    TEXT NOT NULL DEFAULT '',
			UNIQUE (owner_id, aternos_id)
		);

		CREATE TABLE IF NOT EXISTS chats (
			chat_id       INTEGER PRIMARY KEY,
			owner_id      INTEGER NOT NULL REFERENCES owners(user_id) ON DELETE CASCADE,
			title         TEXT NOT NULL DEFAULT '',
			pinned_msg_id INTEGER,
			is_active     INTEGER NOT NULL DEFAULT 1,
			created_at    TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS chat_servers (
			chat_id   INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
			server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
			PRIMARY KEY (chat_id, server_id)
		);

		CREATE TABLE IF NOT EXISTS users (
			user_id    INTEGER NOT NULL,
			chat_id    INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
			username   TEXT NOT NULL DEFAULT '',
			full_name  TEXT NOT NULL DEFAULT '',
			has_access INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (user_id, chat_id)
		);
		CREATE INDEX IF NOT EXISTS idx_users_chat ON users (chat_id);

		CREATE TABLE IF NOT EXISTS audit_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_id   INTEGER NOT NULL,
			user_id    INTEGER,
			chat_id    INTEGER,
			server_id  INTEGER,
			action     TEXT NOT NULL,
			details    TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_audit_owner ON audit_log (owner_id, id);

		CREATE TABLE IF NOT EXISTS db_version (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT ''
		);
	`)
	return err
}

// CreateOwner — idempotent upsert.
func (d *DB) CreateOwner(userID int64, username, fullName, session string) error {
	now := nowISO()
	_, err := d.db.Exec(
		"INSERT INTO owners (user_id, username, full_name, aternos_session,"+
			" created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)"+
			" ON CONFLICT(user_id) DO UPDATE SET username = excluded.username,"+
			" full_name = excluded.full_name, aternos_session = excluded.aternos_session,"+
			" session_valid = 1, updated_at = excluded.updated_at",
		userID, username, fullName, session, now, now,
	)
	return err
}

func scanOwner(row *sql.Row) (*Owner, error) {
	var o Owner
	err := row.Scan(&o.UserID, &o.Username, &o.FullName, &o.AternosSession,
		&o.SessionValid, &o.MaxServers, &o.LockdownMode, &o.CreatedAt, &o.UpdatedAt,
		&o.PMPinnedMsgID, &o.ScheduleTime, &o.ScheduleOnce, &o.Lang)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func scanOwners(rows *sql.Rows) ([]*Owner, error) {
	defer rows.Close()
	var out []*Owner
	for rows.Next() {
		var o Owner
		if err := rows.Scan(&o.UserID, &o.Username, &o.FullName, &o.AternosSession,
			&o.SessionValid, &o.MaxServers, &o.LockdownMode, &o.CreatedAt, &o.UpdatedAt,
			&o.PMPinnedMsgID, &o.ScheduleTime, &o.ScheduleOnce, &o.Lang); err != nil {
			return nil, err
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

func (d *DB) GetOwner(userID int64) (*Owner, error) {
	row := d.db.QueryRow(
		"SELECT user_id, username, full_name, aternos_session, session_valid,"+
			" max_servers, lockdown_mode, created_at, updated_at, pm_pinned_msg_id,"+
			" schedule_time, schedule_once, lang"+
			" FROM owners WHERE user_id = ?", userID)
	o, err := scanOwner(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return o, err
}

func (d *DB) GetAllOwners() ([]*Owner, error) {
	rows, err := d.db.Query(
		"SELECT user_id, username, full_name, aternos_session, session_valid," +
			" max_servers, lockdown_mode, created_at, updated_at, pm_pinned_msg_id," +
			" schedule_time, schedule_once, lang" +
			" FROM owners ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	return scanOwners(rows)
}

func (d *DB) IsOwner(userID int64) (bool, error) {
	var one int
	err := d.db.QueryRow("SELECT 1 FROM owners WHERE user_id = ?", userID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (d *DB) SetOwnerLang(userID int64, lang string) error {
	_, err := d.db.Exec("UPDATE owners SET lang = ? WHERE user_id = ?", lang, userID)
	return err
}

// "18:00" + once — однократный запуск; пустой time отключает.
func (d *DB) SetOwnerSchedule(userID int64, scheduleTime string, once bool) error {
	_, err := d.db.Exec("UPDATE owners SET schedule_time = ?, schedule_once = ?, updated_at = ? WHERE user_id = ?",
		scheduleTime, boolInt(once), nowISO(), userID)
	return err
}

func (d *DB) GetScheduledOwners() ([]*Owner, error) {
	rows, err := d.db.Query(
		"SELECT user_id, username, full_name, aternos_session, session_valid," +
			" max_servers, lockdown_mode, created_at, updated_at, pm_pinned_msg_id," +
			" schedule_time, schedule_once, lang" +
			" FROM owners WHERE schedule_time != '' AND session_valid = 1")
	if err != nil {
		return nil, err
	}
	return scanOwners(rows)
}

func (d *DB) IsChatOwner(chatID, userID int64) (bool, error) {
	chat, err := d.GetChat(chatID)
	if err != nil || chat == nil {
		return false, err
	}
	return chat.OwnerID == userID, nil
}

func (d *DB) UpdateOwnerSession(userID int64, encrypted string) error {
	_, err := d.db.Exec(
		"UPDATE owners SET aternos_session = ?, session_valid = 1, updated_at = ? WHERE user_id = ?",
		encrypted, nowISO(), userID)
	return err
}

func (d *DB) UpdateOwnerProfile(userID int64, username, fullName string) error {
	_, err := d.db.Exec(
		"UPDATE owners SET username = ?, full_name = ?, updated_at = ? WHERE user_id = ?",
		username, fullName, nowISO(), userID)
	return err
}

func (d *DB) SetOwnerLockdown(userID int64, active bool) error {
	_, err := d.db.Exec(
		"UPDATE owners SET lockdown_mode = ?, updated_at = ? WHERE user_id = ?",
		boolInt(active), nowISO(), userID)
	return err
}

func (d *DB) GetOwnerLockdown(userID int64) (bool, error) {
	var v int
	err := d.db.QueryRow("SELECT lockdown_mode FROM owners WHERE user_id = ?", userID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v != 0, err
}

func (d *DB) SetOwnerPmPinned(userID int64, msgID int64) error {
	_, err := d.db.Exec(
		"UPDATE owners SET pm_pinned_msg_id = ?, updated_at = ? WHERE user_id = ?",
		msgID, nowISO(), userID)
	return err
}

func (d *DB) GetOwnerPmPinned(userID int64) (int64, error) {
	var v sql.NullInt64
	err := d.db.QueryRow("SELECT pm_pinned_msg_id FROM owners WHERE user_id = ?", userID).Scan(&v)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Int64, nil
}

// Каскадом удаляются серверы/чаты/участники (ON DELETE CASCADE в схеме).
func (d *DB) DeleteOwner(userID int64) error {
	_, err := d.db.Exec("DELETE FROM owners WHERE user_id = ?", userID)
	return err
}

func scanServer(row interface{ Scan(...any) error }) (*Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.OwnerID, &s.AternosID, &s.ServerIP, &s.DisplayName,
		&s.IsActive, &s.AutoBackupH, &s.AutoConfirm, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func scanServers(rows *sql.Rows) ([]*Server, error) {
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		var s Server
		if err := rows.Scan(&s.ID, &s.OwnerID, &s.AternosID, &s.ServerIP, &s.DisplayName,
			&s.IsActive, &s.AutoBackupH, &s.AutoConfirm, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// Без лимита: на Aternos может быть сколько угодно серверов.
func (d *DB) AddServer(ownerID int64, aternosID, serverIP, displayName string) (int64, error) {
	_, err := d.db.Exec(
		"INSERT OR IGNORE INTO servers (owner_id, aternos_id, server_ip, display_name, created_at)"+
			" VALUES (?, ?, ?, ?, ?)",
		ownerID, aternosID, serverIP, displayName, nowISO())
	if err != nil {
		return 0, err
	}
	server, err := d.GetServerByAternosID(ownerID, aternosID)
	if err != nil || server == nil {
		return 0, err
	}
	return server.ID, nil
}

func (d *DB) GetServer(serverID int64) (*Server, error) {
	row := d.db.QueryRow(
		"SELECT id, owner_id, aternos_id, server_ip, display_name, is_active,"+
			" auto_backup_h, auto_confirm, created_at FROM servers WHERE id = ?", serverID)
	s, err := scanServer(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

func (d *DB) GetServerByAternosID(ownerID int64, aternosID string) (*Server, error) {
	row := d.db.QueryRow(
		"SELECT id, owner_id, aternos_id, server_ip, display_name, is_active,"+
			" auto_backup_h, auto_confirm, created_at FROM servers"+
			" WHERE owner_id = ? AND aternos_id = ?", ownerID, aternosID)
	s, err := scanServer(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

func (d *DB) GetServersByOwner(ownerID int64) ([]*Server, error) {
	rows, err := d.db.Query(
		"SELECT id, owner_id, aternos_id, server_ip, display_name, is_active,"+
			" auto_backup_h, auto_confirm, created_at FROM servers WHERE owner_id = ? ORDER BY id", ownerID)
	if err != nil {
		return nil, err
	}
	return scanServers(rows)
}

func (d *DB) GetActiveServersByOwner(ownerID int64) ([]*Server, error) {
	rows, err := d.db.Query(
		"SELECT id, owner_id, aternos_id, server_ip, display_name, is_active,"+
			" auto_backup_h, auto_confirm, created_at FROM servers"+
			" WHERE owner_id = ? AND is_active = 1 ORDER BY id", ownerID)
	if err != nil {
		return nil, err
	}
	return scanServers(rows)
}

func (d *DB) SetServerActive(serverID int64, active bool) error {
	_, err := d.db.Exec("UPDATE servers SET is_active = ? WHERE id = ?", boolInt(active), serverID)
	return err
}

func (d *DB) DeactivateServer(serverID int64) error {
	_, err := d.db.Exec("UPDATE servers SET is_active = 0 WHERE id = ?", serverID)
	return err
}

func (d *DB) UnbindServerFromAllChats(serverID int64) error {
	_, err := d.db.Exec("DELETE FROM chat_servers WHERE server_id = ?", serverID)
	return err
}

func (d *DB) UpdateServerName(serverID int64, displayName string) error {
	_, err := d.db.Exec("UPDATE servers SET display_name = ? WHERE id = ?", displayName, serverID)
	return err
}

func (d *DB) SetServerAutoConfirm(serverID int64, enabled bool) error {
	_, err := d.db.Exec("UPDATE servers SET auto_confirm = ? WHERE id = ?", boolInt(enabled), serverID)
	return err
}

// Каскадом удаляются связки (ON DELETE CASCADE в схеме).
func (d *DB) RemoveServer(serverID int64) error {
	_, err := d.db.Exec("DELETE FROM servers WHERE id = ?", serverID)
	return err
}

// Upsert последнего известного Minecraft-порта.
func (d *DB) SetServerPort(serverID int64, mcPort int) error {
	_, err := d.db.Exec(
		"INSERT INTO server_meta (server_id, mc_port, updated_at) VALUES (?, ?, ?)"+
			" ON CONFLICT(server_id) DO UPDATE SET mc_port = excluded.mc_port, updated_at = excluded.updated_at",
		serverID, mcPort, nowISO())
	return err
}

func (d *DB) GetServerPort(serverID int64) (int, error) {
	var v sql.NullInt64
	err := d.db.QueryRow("SELECT mc_port FROM server_meta WHERE server_id = ?", serverID).Scan(&v)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

func scanChat(row interface{ Scan(...any) error }) (*Chat, error) {
	var c Chat
	err := row.Scan(&c.ChatID, &c.OwnerID, &c.Title, &c.PinnedMsgID, &c.IsActive, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func scanChats(rows *sql.Rows) ([]*Chat, error) {
	defer rows.Close()
	var out []*Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ChatID, &c.OwnerID, &c.Title, &c.PinnedMsgID, &c.IsActive, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// False — чат занят другим владельцем.
func (d *DB) AddChat(chatID, ownerID int64, title string) (bool, error) {
	chat, err := d.GetChat(chatID)
	if err != nil {
		return false, err
	}
	if chat != nil {
		return chat.OwnerID == ownerID, nil
	}
	_, err = d.db.Exec(
		"INSERT OR IGNORE INTO chats (chat_id, owner_id, title, created_at) VALUES (?, ?, ?, ?)",
		chatID, ownerID, title, nowISO())
	return err == nil, err
}

func (d *DB) GetChat(chatID int64) (*Chat, error) {
	row := d.db.QueryRow(
		"SELECT chat_id, owner_id, title, pinned_msg_id, is_active, created_at"+
			" FROM chats WHERE chat_id = ?", chatID)
	c, err := scanChat(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (d *DB) GetChatOwner(chatID int64) (*Owner, error) {
	row := d.db.QueryRow(
		"SELECT o.user_id, o.username, o.full_name, o.aternos_session, o.session_valid,"+
			" o.max_servers, o.lockdown_mode, o.created_at, o.updated_at, o.pm_pinned_msg_id,"+
			" o.schedule_time, o.schedule_once, o.lang"+
			" FROM chats c JOIN owners o ON o.user_id = c.owner_id WHERE c.chat_id = ?", chatID)
	o, err := scanOwner(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return o, err
}

func (d *DB) ChatExists(chatID int64) (bool, error) {
	var one int
	err := d.db.QueryRow("SELECT 1 FROM chats WHERE chat_id = ?", chatID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (d *DB) GetChatsByOwner(ownerID int64) ([]*Chat, error) {
	rows, err := d.db.Query(
		"SELECT chat_id, owner_id, title, pinned_msg_id, is_active, created_at"+
			" FROM chats WHERE owner_id = ? AND is_active = 1 ORDER BY chat_id", ownerID)
	if err != nil {
		return nil, err
	}
	return scanChats(rows)
}

func (d *DB) SetChatTitle(chatID int64, title string) error {
	_, err := d.db.Exec("UPDATE chats SET title = ? WHERE chat_id = ?", title, chatID)
	return err
}

func (d *DB) SetChatPinnedMsg(chatID, msgID int64) error {
	_, err := d.db.Exec("UPDATE chats SET pinned_msg_id = ? WHERE chat_id = ?", msgID, chatID)
	return err
}

func (d *DB) GetChatPinnedMsg(chatID int64) (int64, error) {
	var v sql.NullInt64
	err := d.db.QueryRow("SELECT pinned_msg_id FROM chats WHERE chat_id = ?", chatID).Scan(&v)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Int64, nil
}

func (d *DB) SetChatActive(chatID int64, active bool) error {
	_, err := d.db.Exec("UPDATE chats SET is_active = ? WHERE chat_id = ?", boolInt(active), chatID)
	return err
}

func (d *DB) RemoveChat(chatID int64) error {
	_, err := d.db.Exec("DELETE FROM chats WHERE chat_id = ?", chatID)
	return err
}

func (d *DB) LinkServerToChat(chatID, serverID int64) error {
	_, err := d.db.Exec("INSERT OR IGNORE INTO chat_servers (chat_id, server_id) VALUES (?, ?)", chatID, serverID)
	return err
}

func (d *DB) UnlinkServerFromChat(chatID, serverID int64) error {
	_, err := d.db.Exec("DELETE FROM chat_servers WHERE chat_id = ? AND server_id = ?", chatID, serverID)
	return err
}

func (d *DB) GetChatServerIDs(chatID int64) ([]int64, error) {
	rows, err := d.db.Query("SELECT server_id FROM chat_servers WHERE chat_id = ?", chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) GetChatServers(chatID int64) ([]*Server, error) {
	rows, err := d.db.Query(
		"SELECT s.id, s.owner_id, s.aternos_id, s.server_ip, s.display_name,"+
			" s.is_active, s.auto_backup_h, s.auto_confirm, s.created_at"+
			" FROM servers s JOIN chat_servers cs ON cs.server_id = s.id"+
			" WHERE cs.chat_id = ? AND s.is_active = 1 ORDER BY s.id", chatID)
	if err != nil {
		return nil, err
	}
	return scanServers(rows)
}

func (d *DB) GetChatsForServer(serverID int64) ([]*Chat, error) {
	rows, err := d.db.Query(
		"SELECT c.chat_id, c.owner_id, c.title, c.pinned_msg_id, c.is_active, c.created_at"+
			" FROM chats c JOIN chat_servers cs ON cs.chat_id = c.chat_id"+
			" WHERE cs.server_id = ? AND c.is_active = 1", serverID)
	if err != nil {
		return nil, err
	}
	return scanChats(rows)
}

func (d *DB) IsServerLinkedToChat(chatID, serverID int64) (bool, error) {
	var one int
	err := d.db.QueryRow("SELECT 1 FROM chat_servers WHERE chat_id = ? AND server_id = ?", chatID, serverID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// Права сохраняются при обновлении имени.
func (d *DB) UpsertChatUser(chatID, userID int64, username, fullName string) error {
	_, err := d.db.Exec(
		"INSERT INTO users (user_id, chat_id, username, full_name, created_at)"+
			" VALUES (?, ?, ?, ?, ?)"+
			" ON CONFLICT(user_id, chat_id) DO UPDATE SET"+
			" username = excluded.username, full_name = excluded.full_name",
		userID, chatID, username, fullName, nowISO())
	return err
}

// RevokeAllAccess мгновенно отбирает право управления (has_access=0)
// у ВСЕХ пользователей всех чатов (экстренный локдаун).
func (d *DB) RevokeAllAccess() error {
	_, err := d.db.Exec("UPDATE users SET has_access = 0")
	return err
}

func (d *DB) GetUserAccess(userID, chatID int64) (bool, error) {
	var v int
	err := d.db.QueryRow("SELECT has_access FROM users WHERE user_id = ? AND chat_id = ?", userID, chatID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v != 0, err
}

// Возвращает 0, если пользователь никогда не писал в чат.
func (d *DB) GetUserIDByUsername(chatID int64, username string) (int64, error) {
	var id int64
	err := d.db.QueryRow(
		"SELECT user_id FROM users WHERE chat_id = ? AND LOWER(username) = LOWER(?) LIMIT 1",
		chatID, username).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (d *DB) SetUserAccess(userID, chatID int64, hasAccess bool) error {
	if _, err := d.db.Exec(
		"INSERT INTO users (user_id, chat_id, created_at) VALUES (?, ?, ?)"+
			" ON CONFLICT(user_id, chat_id) DO NOTHING",
		userID, chatID, nowISO()); err != nil {
		return err
	}
	_, err := d.db.Exec("UPDATE users SET has_access = ? WHERE user_id = ? AND chat_id = ?",
		boolInt(hasAccess), userID, chatID)
	return err
}

// UserCanManage — может ли пользователь управлять серверами в чате.
//
// Владелец чата — всегда True (включая lockdown). Для остальных —
// has_access=1 и lockdown выключен.
func (d *DB) UserCanManage(userID, chatID int64) (bool, error) {
	chat, err := d.GetChat(chatID)
	if err != nil || chat == nil {
		return false, err
	}
	if chat.OwnerID == userID {
		return true, nil
	}
	lockdown, err := d.GetOwnerLockdown(chat.OwnerID)
	if err != nil {
		return false, err
	}
	if lockdown {
		return false, nil
	}
	return d.GetUserAccess(userID, chatID)
}

func (d *DB) GetChatUsersPaginated(chatID int64, limit, offset int) ([]*ChatUser, error) {
	if offset < 0 {
		offset = 0
	}
	rows, err := d.db.Query(
		"SELECT user_id, chat_id, username, full_name, has_access, created_at"+
			" FROM users WHERE chat_id = ?"+
			" ORDER BY has_access DESC, full_name COLLATE NOCASE, user_id LIMIT ? OFFSET ?",
		chatID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ChatUser
	for rows.Next() {
		var u ChatUser
		if err := rows.Scan(&u.UserID, &u.ChatID, &u.Username, &u.FullName, &u.HasAccess, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

func (d *DB) GetChatUsersCount(chatID int64) (int, error) {
	var n int
	err := d.db.QueryRow("SELECT COUNT(*) FROM users WHERE chat_id = ?", chatID).Scan(&n)
	return n, err
}

func (d *DB) LogAction(ownerID int64, action, details string, userID, chatID, serverID int64) error {
	_, err := d.db.Exec(
		"INSERT INTO audit_log (owner_id, user_id, chat_id, server_id, action, details, created_at)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?)",
		ownerID, nullIfZero(userID), nullIfZero(chatID), nullIfZero(serverID), action, details, nowISO())
	return err
}

func (d *DB) GetAuditLog(ownerID int64, limit int) ([]*AuditEntry, error) {
	rows, err := d.db.Query(
		"SELECT id, owner_id, user_id, chat_id, server_id, action, details, created_at"+
			" FROM audit_log WHERE owner_id = ? ORDER BY id DESC LIMIT ?", ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.OwnerID, &e.UserID, &e.ChatID, &e.ServerID,
			&e.Action, &e.Details, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (d *DB) GetStartCount(ownerID int64) (int, error) {
	var n int
	err := d.db.QueryRow(
		"SELECT COUNT(*) FROM audit_log WHERE owner_id = ? AND action = 'server_start'",
		ownerID).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (d *DB) GetStartCountSince(ownerID int64, since time.Time) (int, error) {
	var n int
	err := d.db.QueryRow(
		"SELECT COUNT(*) FROM audit_log WHERE owner_id = ? AND action = 'server_start' AND created_at >= ?",
		ownerID, since.UTC().Format("2006-01-02T15:04:05")).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (d *DB) GetRecentStarts(ownerID int64, limit int) ([]*AuditEntry, error) {
	rows, err := d.db.Query(
		"SELECT id, owner_id, user_id, chat_id, server_id, action, details, created_at"+
			" FROM audit_log WHERE owner_id = ? AND action = 'server_start'"+
			" ORDER BY id DESC LIMIT ?", ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.OwnerID, &e.UserID, &e.ChatID, &e.ServerID,
			&e.Action, &e.Details, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
