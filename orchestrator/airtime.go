package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

const airtimeTimeout = 15 * time.Second

type campaignAirtime struct {
	campaignID     string
	at, next       time.Time
	checking       bool
	offset         int
	attempt        int
	checked, total int
	streams        []twitch.Stream
	failed         bool
	reason         string
}

func (a *campaignAirtime) includes(previous []twitch.Stream) bool {
	for _, old := range previous {
		found := false
		for _, current := range a.streams {
			if old.ChannelID == current.ChannelID && old.BroadcastID == current.BroadcastID && old.GameID == current.GameID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (a *campaignAirtime) status() string {
	switch {
	case a == nil || a.at.IsZero():
		return "Ожидание первой проверки эфиров кампании."
	case len(a.streams) > 0:
		return fmt.Sprintf("Подтверждённых эфиров кампании: %d.", len(a.streams))
	case a.failed:
		return "Не удалось проверить доступность эфиров кампании. " + a.reason + "."
	case a.total > 0:
		return fmt.Sprintf("Ожидание эфира кампании. Подходящих среди проверенных каналов: 0 из %d (в списке %d).", a.checked, a.total)
	default:
		return "Ожидание эфира кампании. Twitch не показал эфиры с Drops в категории."
	}
}

func (a *campaignAirtime) summary() string {
	message := a.status()
	if a == nil || a.at.IsZero() {
		return message
	}
	return message + " Проверено в " + a.at.Local().Format("15:04 MST") + "."
}

func (s *scheduler) airtimeFor(key, campaignID string) *campaignAirtime {
	air := s.airtimes[key]
	if air != nil && air.campaignID == campaignID {
		return air
	}
	return nil
}

// checkCampaignAirtime is called with the scheduler locked.
func (s *scheduler) checkCampaignAirtime(ctx context.Context, key string, g *gameState, c *campaign, now time.Time) *campaignAirtime {
	air := s.airtimeFor(key, c.ID)
	if air == nil {
		air = &campaignAirtime{campaignID: c.ID}
		if s.airtimes == nil {
			s.airtimes = make(map[string]*campaignAirtime)
		}
		s.airtimes[key] = air
	}
	if air.checking || now.Before(air.next) || s.airtimeChecks >= 4 || ctx.Err() != nil {
		return air
	}
	var available []parser.Account
	var own []parser.Account
	for _, account := range s.state.Accounts {
		if !account.Quarantined {
			if account.Game == key {
				own = append(own, account.Credentials)
			} else {
				available = append(available, account.Credentials)
			}
		}
	}
	available = append(own, available...)
	if len(available) == 0 {
		return air
	}
	credentials := available[air.attempt%len(available)]
	air.attempt++
	air.checking = true
	s.airtimeChecks++
	cfg := s.miner
	game, campaignID, campaignName, offset := g.Name, c.ID, c.Name, air.offset
	go func() {
		checkCtx, cancel := context.WithTimeout(ctx, airtimeTimeout)
		defer cancel()
		client := &http.Client{Timeout: airtimeTimeout}
		defer client.CloseIdleConnections()
		streams, checked, total, err := twitch.NewGQLClient(client, &cfg, &credentials).CampaignStreams(checkCtx, game, campaignID, offset)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.airtimeChecks--
		if ctx.Err() != nil || s.state.Games[key] != g || s.airtimes[key] != air {
			return
		}
		previous := air.status()
		wasReady, wasFailed, wasChecked := len(air.streams) > 0, air.failed, !air.at.IsZero()
		air.at = time.Now()
		air.next = air.at.Add(time.Duration(s.state.Settings.DiscoverySeconds) * time.Second)
		air.checking, air.failed = false, err != nil
		air.reason = ""
		if err != nil {
			air.reason = err.Error()
		}
		air.streams, air.checked, air.total = streams, checked, total
		if len(streams) == 0 && checked > 0 && checked < total {
			air.offset = (offset + checked) % total
		}
		ready := len(streams) > 0
		if !wasChecked || wasReady != ready || (!ready && wasFailed != air.failed) {
			message := air.status()
			s.event("campaign_airtime game=%q campaign=%q previous=%q status=%q", game, campaignName, previous, message)
			if !g.Paused && !g.Removing {
				s.sendNotice("airtime", fmt.Sprintf("«%s» · %s\n%s", game, campaignName, message))
			}
		}
	}()
	return air
}
