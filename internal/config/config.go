// Package config — loads bot settings from .env.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken       string
	SuperAdminID   int64
	EncryptionKey  string
	DBPath         string
	TraceMem       bool
	UpdateInterval int
	SessionCheck   int
}

// .env next to the binary/working directory; validates required fields.
func Load() (*Config, error) {
	_ = godotenv.Load(".env")

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("BOT_TOKEN is missing in .env")
	}
	// Bot superadmin: SUPER_ADMIN_ID (legacy alias OWNER_ID). Receives
	// error notifications, can send /announce. Other
	// users go through onboarding via /start.
	adminRaw := strings.TrimSpace(os.Getenv("SUPER_ADMIN_ID"))
	if adminRaw == "" {
		adminRaw = strings.TrimSpace(os.Getenv("OWNER_ID"))
	}
	adminID, err := strconv.ParseInt(adminRaw, 10, 64)
	if err != nil || adminID <= 0 {
		return nil, fmt.Errorf("SUPER_ADMIN_ID (or OWNER_ID) is missing or invalid in .env")
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
