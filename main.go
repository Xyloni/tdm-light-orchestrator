package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/xyloni/tdm-light-orchestrator/orchestrator"
	"runtime"
	"syscall"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func main() {
	log.SetPrefix("[tdm-light] ")
	log.SetFlags(log.LstdFlags)
	if len(os.Args) == 2 && (os.Args[1] == "--orchestrator" || os.Args[1] == "--managed-miner") {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if os.Args[1] == "--managed-miner" {
			if err := orchestrator.RunMiner(ctx, os.Stdin, os.Stdout); err != nil {
				log.Fatal(err)
			}
			return
		}
		dir := os.Getenv("ORCHESTRATOR_DIR")
		if dir == "" {
			dir = filepath.Join(".", "orchestrator-data")
		}
		address := os.Getenv("ORCHESTRATOR_WEB_ADDR")
		if address == "" {
			address = "127.0.0.1:8787"
		}
		if err := orchestrator.Run(ctx, dir, address); err != nil {
			log.Fatal(err)
		}
		return
	}
	explicitSetup := len(os.Args) == 2 && os.Args[1] == "--setup"
	if err := config.Setup("config.json", "config.env", os.Stdin, os.Stdout, explicitSetup); err != nil {
		log.Fatalf("настройка: %v", err)
	}
	if explicitSetup {
		return
	}

	// Graceful shutdown по SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Канал горячей перезагрузки (команда /restart из Telegram).
	reloadCh := make(chan struct{}, 1)
	pollState := new(bot.PollState)
	previousBotToken := ""

	for {
		cfg, err := config.Load("config.json", "config.env")
		if err != nil {
			log.Fatalf("конфигурация: %v", err)
		}
		if err := parser.MigrateLegacyFile(cfg.AccountsFile); err != nil {
			log.Fatalf("перенос файла аккаунтов: %v", err)
		}
		groups, err := parser.ParseFile(cfg.AccountsFile)
		if err != nil {
			if errors.Is(err, parser.ErrNoAccounts) || os.IsNotExist(err) {
				log.Printf("список аккаунтов пуст или отсутствует (%s); можно загрузить account.txt через выбранного бота", cfg.AccountsFile)
				groups = nil
			} else {
				log.Fatalf("аккаунты: %v", err)
			}
		}

		if cfg.TelegramBotToken != previousBotToken {
			pollState = new(bot.PollState)
			previousBotToken = cfg.TelegramBotToken
		}
		var combined io.WriteCloser
		previousOutput := log.Writer()
		if cfg.SingleLog && cfg.LogDir != "" {
			combined, err = worker.OpenUnifiedLog(cfg.LogDir, previousOutput)
			if err != nil {
				log.Printf("единый лог недоступен: %v", err)
			} else {
				log.SetOutput(combined)
			}
		}
		diagnosticCtx, stopDiagnostics := context.WithCancel(ctx)
		diagnosticDone := make(chan struct{})
		go func() {
			defer close(diagnosticDone)
			if !cfg.Diagnostics {
				return
			}
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			segments := time.NewTicker(time.Minute)
			defer segments.Stop()
			for {
				select {
				case <-diagnosticCtx.Done():
					return
				case <-segments.C:
					twitch.FlushSegmentDiagnostics()
				case <-ticker.C:
					var ms runtime.MemStats
					runtime.ReadMemStats(&ms)
					log.Printf("diagnostic runtime goroutines=%d heap_mb=%.1f heap_sys_mb=%.1f gc=%d gc_pause_total_ms=%.1f", runtime.NumGoroutine(), float64(ms.HeapAlloc)/(1<<20), float64(ms.HeapSys)/(1<<20), ms.NumGC, float64(ms.PauseTotalNs)/1e6)
				}
			}
		}()
		log.Printf("logging single=%t diagnostics=%t", cfg.SingleLog, cfg.Diagnostics)
		mgr := worker.NewManager(cfg, groups, reloadCh, pollState)
		reload := mgr.Run(ctx)
		stopDiagnostics()
		<-diagnosticDone
		if cfg.Diagnostics {
			twitch.FlushSegmentDiagnostics()
		}
		if reload {
			log.Println("горячая перезагрузка config.json и account.txt...")
		} else {
			log.Println("сервис остановлен")
		}
		log.SetOutput(previousOutput)
		if combined != nil {
			combined.Close()
		}
		if !reload {
			break // shutdown
		}
	}
}
