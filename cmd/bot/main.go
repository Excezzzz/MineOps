// MineOps — бот управления серверами Aternos (Go-порт).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-co-op/gocron"

	"mineops/internal/aternos"
	"mineops/internal/config"
	"mineops/internal/crypto"
	"mineops/internal/dashboard"
	"mineops/internal/database"
	"mineops/internal/queuewatcher"
	"mineops/internal/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := crypto.Init(cfg.EncryptionKey); err != nil {
		log.Fatalf("crypto: %v", err)
	}

	db, err := database.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("база данных: %v", err)
	}
	defer db.Close()
	log.Printf("БД: %s (схема v%d)", cfg.DBPath, database.SchemaVersion)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	managers := aternos.NewRegistry(db, httpClient)
	dash := dashboard.New(db, managers, telegram.DashboardKB)
	watcher := queuewatcher.New(db, managers, dash)

	bot, err := telegram.NewBot(cfg, db, managers, dash, watcher)
	if err != nil {
		log.Fatalf("telebot: %v", err)
	}
	// Перехватчик ошибок аутентификации Aternos: уведомляем Владельца в ЛС.
	managers.SetAuthHook(func(ownerID int64) {
		bot.NotifySessionExpired(ownerID)
	})

	// Планировщик: дашборды каждые 30 секунд, проверка сессий каждые 5 минут.
	s := gocron.NewScheduler(time.UTC)
	s.SingletonMode()
	_, err = s.Every(cfg.UpdateInterval).Seconds().Do(func() {
		dash.UpdateDashboards(context.Background(), bot.Bot())
	})
	if err != nil {
		log.Printf("планировщик (дашборд): %v", err)
	}
	_, err = s.Every(cfg.SessionCheck).Seconds().Do(func() {
		dash.CheckAllSessions(context.Background(), bot.Bot())
	})
	if err != nil {
		log.Printf("планировщик (сессии): %v", err)
	}
	s.StartAsync()
	log.Printf("планировщик запущен: дашборд %ds, сессии %ds", cfg.UpdateInterval, cfg.SessionCheck)

	log.Printf("бот запущен (pid %d)", os.Getpid())
	// Восстановить наблюдение за серверами, которые уже в очереди Aternos
	// (watcher'ы живут в памяти и теряются при перезапуске бота).
	go func() {
		time.Sleep(5 * time.Second)
		watcher.Rescan(bot.Bot())
	}()

	// Graceful shutdown: SIGINT/SIGTERM → останавливаем планировщик, бота, БД.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("получен сигнал остановки, завершаю работу...")
		s.Stop()
		bot.Bot().Stop()
		_ = db.Close()
		log.Println("shutdown complete")
		os.Exit(0)
	}()

	bot.Start()
}