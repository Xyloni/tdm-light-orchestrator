package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestExclusionCommandsAcrossTelegramAndDiscord(t *testing.T) {
	for _, platform := range []string{"telegram", "discord"} {
		t.Run(platform, func(t *testing.T) {
			changed := []string{}
			reloads := 0
			get := func() []string { return []string{"Hunt White"} }
			set := func(name string, exclude bool) error {
				changed = append(changed, fmt.Sprintf("%s:%v", name, exclude))
				return nil
			}
			if platform == "telegram" {
				b := New("fixture", 1, nil, nil, func() { reloads++ }, nil, nil, nil)
				b.ConfigureExclusions(get, set)
				b.http.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
				})
				for _, command := range []string{"/exclude", "/exclude Other reward", "/include Other reward"} {
					b.handle(context.Background(), command, nil)
				}
			} else {
				d := testDiscord()
				d.actions.Excluded = get
				d.actions.SetExcluded = set
				for _, command := range []struct{ name, value string }{{"exclude", ""}, {"exclude", "Other reward"}, {"include", "Other reward"}} {
					i := interaction(command.name)
					data := map[string]any{"name": command.name}
					if command.value != "" {
						data["options"] = []map[string]string{{"name": "name", "value": command.value}}
					}
					encoded, _ := json.Marshal(data)
					_ = json.Unmarshal(encoded, &i.Data)
					text, reload := d.command(context.Background(), i)
					if text == "" {
						t.Fatal("empty command reply")
					}
					if reload {
						reloads++
					}
				}
			}
			if reloads != 2 || strings.Join(changed, "|") != "Other reward:true|Other reward:false" {
				t.Fatal("incorrect exclusion commands", reloads, changed)
			}
		})
	}
}

func TestFailedExclusionSaveDoesNotReload(t *testing.T) {
	_, reload := exclusionCommand("Reward", true, nil, func(string, bool) error { return fmt.Errorf("read only") })
	if reload {
		t.Fatal("failed save requested reload")
	}
	text, reload := exclusionCommand("", true, func() []string { return []string{"Hunt White"} }, nil)
	if reload || !strings.Contains(text, "Hunt White") {
		t.Fatal("list command failed")
	}
}
