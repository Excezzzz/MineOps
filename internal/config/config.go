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
	OwnerID        int64
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
	// Единственный владелец приватного бота: OWNER_ID (legacy-алиас SUPER_ADMIN_ID).
	ownerRaw := strings.TrimSpace(os.Getenv("OWNER_ID"))
	if ownerRaw == "" {
		ownerRaw = strings.TrimSpace(os.Getenv("SUPER_ADMIN_ID"))
	}
	ownerID, err := strconv.ParseInt(ownerRaw, 10, 64)
	if err != nil || ownerID <= 0 {
		return nil, fmt.Errorf("OWNER_ID (или SUPER_ADMIN_ID) is missing or invalid in .env")
	}

	cfg := &Config{
		BotToken:       token,
		SuperAdminID:   ownerID,
		OwnerID:        ownerID,
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