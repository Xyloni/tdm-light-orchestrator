package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type CampaignDirectoryCursor struct {
	drops, all string
}

// CampaignStreams checks this campaign's presence on each candidate channel.
// offset rotates through large creator allowlists between polls.
func (c *GQLClient) CampaignStreams(ctx context.Context, game, campaignID string, offset int, directory *CampaignDirectoryCursor) (streams []Stream, checked, total int, status string, err error) {
	type campaignUser struct {
		DropCampaign *struct {
			Allow *struct {
				IsEnabled *bool
				Channels  []struct{ Name string }
			}
		}
	}
	var details struct {
		User *campaignUser
	}
	detailsErr := c.campaignQuery(ctx, "DropCampaignDetails", "039277bf98f3130929262cc7c6efd9c141ca3749cb6dca442fc8ead9a53f77c1",
		map[string]any{"channelLogin": c.Account.Login, "dropID": campaignID}, &details)
	if detailsErr != nil || details.User == nil || details.User.DropCampaign == nil {
		const query = `query OrchestratorCampaignChannels($id: ID!) {
			user: currentUser { dropCampaign(id: $id) { allow { isEnabled channels { name } } } }
		}`
		details.User = nil
		detailsErr = c.do(ctx, "OrchestratorCampaignChannels", query, map[string]any{"id": campaignID}, &details)
		if detailsErr != nil || details.User == nil || details.User.DropCampaign == nil {
			detailsErr = errors.New("Twitch не подтвердил список каналов кампании")
		}
	}
	var allowlist []string
	if detailsErr == nil {
		allow := details.User.DropCampaign.Allow
		if allow != nil && (allow.IsEnabled == nil || *allow.IsEnabled) {
			for _, channel := range allow.Channels {
				allowlist = append(allowlist, channel.Name)
			}
		}
	}
	queriedChannels := make(map[string]bool)
	check := func(stream Stream) {
		if stream.ChannelID == "" || stream.BroadcastID == "" || stream.Login == "" ||
			(stream.Game != "" && !strings.EqualFold(stream.Game, game)) {
			return
		}
		if queriedChannels[stream.ChannelID] {
			return
		}
		ok, lookupErr := c.channelHasCampaign(ctx, stream.ChannelID, campaignID)
		if lookupErr != nil {
			err = errors.New("Twitch не подтвердил кампанию на проверяемых каналах")
			return
		}
		queriedChannels[stream.ChannelID] = true
		if !ok {
			return
		}
		if stream.GameID == "" || stream.Game == "" {
			metadata, metadataErr := c.StreamMetadata(ctx, stream.Login)
			if errors.Is(metadataErr, ErrStreamOffline) {
				return
			}
			if metadataErr != nil || metadata.GameID == "" || metadata.Game == "" || metadata.BroadcastID == "" || metadata.ChannelID != stream.ChannelID {
				delete(queriedChannels, stream.ChannelID)
				err = errors.New("Twitch не вернул данные подтверждённого эфира кампании")
				return
			}
			if !strings.EqualFold(metadata.Game, game) {
				return
			}
			stream = metadata
		}
		streams = append(streams, stream)
	}
	if len(allowlist) > 0 {
		total = len(allowlist)
		offline := 0
		for i := 0; i < min(total, 30); i++ {
			login := allowlist[(offset+i)%total]
			stream, lookupErr := c.StreamMetadata(ctx, login)
			if lookupErr != nil && !errors.Is(lookupErr, ErrStreamOffline) {
				err = errors.New("Twitch не вернул состояние каналов из списка кампании")
			}
			if ctx.Err() != nil {
				break
			}
			checked++
			if errors.Is(lookupErr, ErrStreamOffline) {
				offline++
			}
			if lookupErr == nil {
				check(stream)
			}
		}
		switch {
		case total == 1 && offline == 1:
			status = fmt.Sprintf("В списке кампании один канал.\n🔴 %s · офлайн", allowlist[0])
		case total == checked && offline == total:
			status = fmt.Sprintf("Все %d каналов из списка кампании сейчас офлайн.", total)
		case len(streams) == 0 && err == nil && ctx.Err() == nil:
			status = fmt.Sprintf("В списке кампании %d каналов. Проверено %d, офлайн %d. Подходящий эфир пока не подтверждён.", total, checked, offline)
		}
	}
	if len(streams) == 0 && ctx.Err() == nil {
		seen := make(map[string]bool)
		if directory == nil {
			directory = &CampaignDirectoryCursor{}
		}
		for _, page := range []struct {
			dropsOnly bool
			cursor    *string
		}{{true, &directory.drops}, {false, &directory.all}} {
			if ctx.Err() != nil || len(streams) > 0 {
				break
			}
			candidates, next, directoryErr := c.categoryStreams(ctx, game, page.dropsOnly, *page.cursor)
			if errors.Is(directoryErr, ErrNoDropsStreams) {
				*page.cursor = next
				continue
			}
			if directoryErr != nil {
				*page.cursor = ""
				err = errors.New("Twitch не вернул каталог эфиров категории")
				continue
			}
			for _, stream := range candidates {
				if ctx.Err() != nil {
					break
				}
				seen[stream.Login] = true
				check(stream)
			}
			if len(streams) == 0 && ctx.Err() == nil {
				*page.cursor = next
			}
		}
		if len(allowlist) == 0 {
			total, checked = len(seen), len(queriedChannels)
		} else if len(streams) == 0 && len(seen) > 0 {
			status += fmt.Sprintf("\nВ каталоге категории тоже нет подтверждённых эфиров кампании (найдено каналов: %d).", len(seen))
		}
		if len(streams) == 0 && (directory.drops != "" || directory.all != "") {
			if status == "" && len(allowlist) == 0 {
				status = fmt.Sprintf("Проверено по ID кампании: %d. Каналов на просмотренных страницах: %d.", checked, total)
			}
			status += "\nПоиск продолжится на следующих страницах каталога."
		}
	}
	if ctx.Err() != nil {
		err = errors.New("Twitch не ответил в пределах времени проверки эфиров")
	} else if len(streams) == 0 && err == nil {
		if detailsErr != nil {
			err = fmt.Errorf("Список каналов кампании недоступен. Каналов в каталоге категории: %d. Проверок по ID кампании: %d. Подходящий эфир не найден", total, checked)
		}
	}
	return
}

func (c *GQLClient) channelHasCampaign(ctx context.Context, channelID, campaignID string) (bool, error) {
	var data struct {
		Channel *struct{ ViewerDropCampaigns []struct{ ID string } }
	}
	const operation = "DropsHighlightService_AvailableDrops"
	variables := map[string]any{"channelID": channelID}
	if err := c.campaignQuery(ctx, operation, "782dad0f032942260171d2d80a654f88bdd0c5a9dddc392e9bc92218a0f42d20", variables, &data); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		const query = `query DropsHighlightService_AvailableDrops($channelID: ID!) {
			channel(id: $channelID) { viewerDropCampaigns { id } }
		}`
		if err := c.do(ctx, operation, query, variables, &data); err != nil {
			return false, err
		}
	}
	if data.Channel == nil {
		return false, errors.New("Twitch не вернул данные канала")
	}
	for _, item := range data.Channel.ViewerDropCampaigns {
		if item.ID == campaignID {
			return true, nil
		}
	}
	return false, nil
}

func (c *GQLClient) campaignQuery(ctx context.Context, operation, hash string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{
		"operationName": operation,
		"variables":     variables,
		"extensions":    map[string]any{"persistedQuery": map[string]any{"version": 1, "sha256Hash": hash}},
	})
	if err != nil {
		return fmt.Errorf("encode %s: %w", operation, err)
	}
	return c.doPayload(ctx, operation, payload, out)
}
