// Package config загружает настройки бота из .env (godotenv).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config — типизированные настройки бота.
type Config struct {
	BotToken       string
	SuperAdminID   int64
	EncryptionKey  string
	DBPath         string
	TraceMem       bool
	UpdateInterval int
	SessionCheck   int
}

// Load читает .env рядом с исполняемым файлом/рабочей директорией и валидирует.
func Load() (*Config, error) {
	_ = godotenv.Load(".env")

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("BOT_TOKEN is missing in .env")
	}
	// Суперадмин бота: SUPER_ADMIN_ID (legacy-алиас OWNER_ID). Получает
	// уведомления об ошибках, может рассылать /announce. Остальные
	// пользователи проходят онбординг через /start.
	adminRaw := strings.TrimSpace(os.Getenv("SUPER_ADMIN_ID"))
	if adminRaw == "" {
		adminRaw = strings.TrimSpace(os.Getenv("OWNER_ID"))
	}
	adminID, err := strconv.ParseInt(adminRaw, 10, 64)
	if err != nil || adminID <= 0 {
		return nil, fmt.Errorf("SUPER_ADMIN_ID (или OWNER_ID) is missing or invalid in .env")
	}

	cfg := &Config{
		BotToken:       token,
		SuperAdminID:   adminID,
		EncryptionKey:  strings.TrimSpace(os.Getenv("ENCRYPTION_KEY")),
		DBPath:         envOr("DB_PATH", "data/mineops.db"),
		TraceMem:       os.Getenv("TRACE_MEM") == "1",
		UpdateInterval: 30,
		SessionCheck:   5 * 60,
	}
	cfg.DBPath = filepath.Clean(cfg.DBPath)
	return cfg, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}