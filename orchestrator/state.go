package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

type settings struct {
	SaveResources       bool
	MaxGames            int
	MaxAccounts         int
	ReserveMinutes      int
	ExpectedRatePercent int
	DiscoverySeconds    int
	MinerOptions        *minerOptions
}

type minerOptions struct {
	Diagnostics bool
	SingleLog   bool
	UsePubSub   bool
}

type account struct {
	Quarantined bool

	Credentials parser.Account
	Game        string
}

type gameState struct {
	AutoClaimDrops   bool
	Mode             string
	ReserveMinutes   int
	ExcludedRewards  []string
	Removing         bool
	DeleteAccounts   bool
	Paused           bool
	Regroup          bool
	Name             string
	Target           int
	Current          string
	SelectedCampaign string
	SeriesPaused     bool
	Campaigns        map[string]*campaign
}

type campaign struct {
	ID, Name, Status         string
	Starts, Ends, Discovered time.Time
	Minutes                  int
	Rewards                  []rewardRequirement
	ObservedRate             float64
	Results                  map[string]string
	Group                    []string
	Finished                 string
	CompletedGroups          int
	PlannedGroupTime         time.Duration
	ActualGroupTime          time.Duration
	DeadlineReportSentFor    time.Time `json:",omitzero"`
}

type state struct {
	TelegramMenu          telegramMenuState
	Digest                digestSettings
	Stats                 statistics
	NotificationsMuted    bool
	NotificationOverrides map[string]bool
	Catalog               catalogHealth
	Bot                   *botSettings
	Settings              settings
	Games                 map[string]*gameState
	Accounts              []account
}

func loadState(path string) (*state, error) {
	s := &state{Settings: settings{MaxGames: 2, MaxAccounts: 1000, ReserveMinutes: 10, ExpectedRatePercent: 75, DiscoverySeconds: 120}, Games: make(map[string]*gameState)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Games == nil {
		s.Games = make(map[string]*gameState)
	}
	if s.Settings.MaxGames < 1 || s.Settings.MaxAccounts < 1 || s.Settings.ExpectedRatePercent < 1 || s.Settings.ExpectedRatePercent > 100 || s.Settings.ReserveMinutes < 1 || s.Settings.DiscoverySeconds < 15 {
		return nil, fmt.Errorf("недопустимые настройки в %s", path)
	}
	return s, nil
}

func saveState(path string, s *state) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".orchestrator-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(s)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func assignPool(s *state, key string) bool {
	target := s.Games[key].Target
	for _, a := range s.Accounts {
		if a.Game == key && !a.Quarantined {
			target--
		}
	}
	changed := false
	for index := range s.Accounts {
		if target <= 0 {
			break
		}
		if s.Accounts[index].Game == "" && !s.Accounts[index].Quarantined {
			s.Accounts[index].Game = key
			target--
			changed = true
		}
	}
	return changed
}

func pending(s *state, key string, c *campaign) []string {
	var result []string
	for _, a := range s.Accounts {
		if a.Game == key && !a.Quarantined && c.Results[a.Credentials.Login] == "" {
			result = append(result, a.Credentials.Login)
		}
	}
	return result
}
