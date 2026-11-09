package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestMinerProcessesMultipleGroupsOnOneConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input, commands := io.Pipe()
	output, events := io.Pipe()
	defer commands.Close()
	defer output.Close()
	done := make(chan error, 1)
	go func() {
		defer input.Close()
		defer events.Close()
		done <- RunMiner(ctx, input, events)
	}()
	enc, dec := json.NewEncoder(commands), json.NewDecoder(output)
	for _, id := range []string{"first", "second"} {
		cfg := config.Default()
		cfg.LogDir = ""
		cfg.CampaignID = id
		// Expired commands exercise the real protocol without contacting Twitch.
		command := minerCommand{Config: cfg, Accounts: []*parser.Account{{Login: "tester"}}, Deadline: time.Now().Add(-time.Minute)}
		if err := enc.Encode(command); err != nil {
			t.Fatal(err)
		}
		var event minerEvent
		if err := dec.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if !event.Done || len(event.Progress) != 1 || event.Progress[0].Status != "stopped" {
			t.Fatalf("%s: unexpected group result %+v", id, event)
		}
	}
	commands.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
