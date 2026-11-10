package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CampaignStreams checks this campaign's presence on each candidate channel.
// offset rotates through large creator allowlists between polls.
func (c *GQLClient) CampaignStreams(ctx context.Context, game, campaignID string, offset int) (streams []Stream, checked, total int, err error) {
	var details struct {
		User *struct {
			DropCampaign *struct {
				Allow *struct {
					IsEnabled *bool
					Channels  []struct{ Name string }
				}
			}
		}
	}
	detailsErr := c.campaignQuery(ctx, "DropCampaignDetails", "039277bf98f3130929262cc7c6efd9c141ca3749cb6dca442fc8ead9a53f77c1",
		map[string]any{"channelLogin": c.Account.Login, "dropID": campaignID}, &details)
	if detailsErr != nil || details.User == nil || details.User.DropCampaign == nil {
		detailsErr = errors.New("Twitch не подтвердил список каналов кампании")
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
	check := func(stream Stream) {
		if !strings.EqualFold(stream.Game, game) || stream.GameID == "" || stream.ChannelID == "" || stream.BroadcastID == "" || stream.Login == "" {
			return
		}
		ok, lookupErr := c.channelHasCampaign(ctx, stream.ChannelID, campaignID)
		if lookupErr != nil {
			err = errors.New("Twitch не подтвердил кампанию на проверяемых каналах")
		} else if ok {
			streams = append(streams, stream)
		}
	}
	if len(allowlist) > 0 {
		total = len(allowlist)
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
			if lookupErr == nil {
				check(stream)
			}
		}
	}
	if len(allowlist) == 0 || (len(streams) == 0 && err != nil && ctx.Err() == nil) {
		var candidates []Stream
		candidates, directoryErr := c.TopStreams(ctx, game, 30)
		if errors.Is(directoryErr, ErrNoDropsStreams) {
			return nil, checked, total, errors.Join(err, detailsErr)
		}
		if directoryErr != nil {
			return nil, 0, 0, errors.New("Twitch не вернул каталог эфиров категории")
		}
		allowed := make(map[string]bool, len(allowlist))
		for _, login := range allowlist {
			allowed[strings.ToLower(login)] = true
		}
		if len(allowed) == 0 {
			total = len(candidates)
		}
		for _, stream := range candidates {
			if ctx.Err() != nil {
				break
			}
			if len(allowed) > 0 && !allowed[strings.ToLower(stream.Login)] {
				continue
			}
			if len(allowed) == 0 {
				checked++
			}
			check(stream)
		}
	}
	if ctx.Err() != nil {
		err = errors.New("Twitch не ответил в пределах времени проверки эфиров")
	} else if len(streams) == 0 && err == nil {
		err = detailsErr
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
