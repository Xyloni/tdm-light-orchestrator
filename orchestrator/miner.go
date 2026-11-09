package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

type minerCommand struct {
	Config   *config.Config
	Accounts []*parser.Account
	Remove   []string
	Deadline time.Time
}

type minerEvent struct {
	Rewards  []string
	Progress []worker.ManagedProgress
	Done     bool
}

type minerProcess struct {
	stop     context.CancelFunc
	commands chan minerCommand
}

type processUpdate struct {
	game    string
	process *minerProcess
	event   minerEvent
	exited  bool
}

func startMiner(ctx context.Context, game string, updates chan<- processUpdate) (*minerProcess, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, path, "--managed-miner")
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		stop()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		stop()
		input.Close()
		output.Close()
		return nil, err
	}
	p := &minerProcess{stop: stop, commands: make(chan minerCommand, 4)}
	go func() {
		defer input.Close()
		enc := json.NewEncoder(input)
		for {
			select {
			case <-ctx.Done():
				return
			case command := <-p.commands:
				if enc.Encode(command) != nil {
					stop()
					return
				}
			}
		}
	}()
	go func() {
		defer stop()
		send := func(update processUpdate) {
			// Exit must reach the scheduler even after the child context is cancelled.
			updates <- update
		}
		dec := json.NewDecoder(output)
		for {
			var event minerEvent
			if dec.Decode(&event) != nil {
				break
			}
			send(processUpdate{game: game, process: p, event: event})
		}
		stop()
		_ = cmd.Wait()
		send(processUpdate{game: game, process: p, exited: true})
	}()
	return p, nil
}

// RunMiner is the private subprocess entry point. A closed pipe stops all work.
func RunMiner(ctx context.Context, input io.Reader, output io.Writer) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	commands := make(chan minerCommand)
	go func() {
		defer stop()
		dec := json.NewDecoder(input)
		for {
			var command minerCommand
			if dec.Decode(&command) != nil {
				return
			}
			select {
			case commands <- command:
			case <-ctx.Done():
				return
			}
		}
	}()
	enc := json.NewEncoder(output)
	for {
		select {
		case <-ctx.Done():
			return nil
		case command := <-commands:
			if len(command.Remove) > 0 {
				continue
			}
			if command.Config == nil || len(command.Accounts) == 0 || command.Deadline.IsZero() {
				return errors.New("invalid group command")
			}
			if err := runMinerGroup(ctx, enc, commands, command); err != nil {
				return err
			}
		}
	}
}

func runMinerGroup(ctx context.Context, enc *json.Encoder, commands <-chan minerCommand, command minerCommand) error {
	groupCtx, cancel := context.WithDeadline(ctx, command.Deadline)
	defer cancel()
	cfg := command.Config
	cfg.BotPlatform = "none"
	cfg.ManagedAuthAttempts = 3
	cfg.DailyLogs = true
	if cfg.SingleLog && cfg.LogDir != "" {
		previous := log.Writer()
		combined := worker.OpenDailyUnifiedLog(cfg.LogDir, previous)
		log.SetOutput(combined)
		defer func() { log.SetOutput(previous); combined.Close() }()
	}
	manager := worker.NewManager(cfg, [][]*parser.Account{command.Accounts}, nil)
	removed := make(chan []string, 1)
	done := make(chan struct{})
	go func() { defer close(done); manager.RunManagedGroup(groupCtx, command.Accounts, removed) }()
	defer func() { cancel(); <-done }()
	rewardTicker := time.NewTicker(time.Minute)
	defer rewardTicker.Stop()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return enc.Encode(minerEvent{Progress: manager.ManagedSnapshot(), Done: true, Rewards: manager.ManagedRewardSummaries()})
		case <-rewardTicker.C:
			if rewards := manager.ManagedRewardSummaries(); len(rewards) > 0 {
				if err := enc.Encode(minerEvent{Progress: manager.ManagedSnapshot(), Rewards: rewards}); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := enc.Encode(minerEvent{Progress: manager.ManagedSnapshot()}); err != nil {
				return err
			}
		case next := <-commands:
			if len(next.Remove) == 0 {
				return errors.New("expected account removal during group")
			}
			select {
			case removed <- next.Remove:
			case <-done:
				return enc.Encode(minerEvent{Progress: manager.ManagedSnapshot(), Done: true, Rewards: manager.ManagedRewardSummaries()})
			case <-ctx.Done():
				return nil
			}
		}
	}
}
