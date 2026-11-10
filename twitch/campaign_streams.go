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
	if err = c.campaignQuery(ctx, "DropCampaignDetails", "039277bf98f3130929262cc7c6efd9c141ca3749cb6dca442fc8ead9a53f77c1",
		map[string]any{"channelLogin": c.Account.Login, "dropID": campaignID}, &details); err != nil {
		return
	}
	if details.User == nil || details.User.DropCampaign == nil {
		return nil, 0, 0, errors.New("Twitch не вернул условия кампании")
	}
	allow := details.User.DropCampaign.Allow
	check := func(stream Stream) {
		if !strings.EqualFold(stream.Game, game) || stream.GameID == "" || stream.ChannelID == "" || stream.BroadcastID == "" || stream.Login == "" {
			return
		}
		ok, lookupErr := c.channelHasCampaign(ctx, stream.ChannelID, campaignID)
		if lookupErr != nil {
			err = lookupErr
		} else if ok {
			streams = append(streams, stream)
		}
	}
	if allow != nil && len(allow.Channels) > 0 && (allow.IsEnabled == nil || *allow.IsEnabled) {
		total = len(allow.Channels)
		for i := 0; i < min(total, 30); i++ {
			login := allow.Channels[(offset+i)%total].Name
			stream, lookupErr := c.StreamMetadata(ctx, login)
			if lookupErr != nil && !errors.Is(lookupErr, ErrStreamOffline) {
				err = lookupErr
			}
			if ctx.Err() != nil {
				break
			}
			checked++
			if lookupErr == nil {
				check(stream)
			}
		}
	} else {
		var candidates []Stream
		candidates, err = c.TopStreams(ctx, game, 30)
		if errors.Is(err, ErrNoDropsStreams) {
			return nil, 0, 0, nil
		}
		if err != nil {
			return nil, 0, 0, err
		}
		total = len(candidates)
		for _, stream := range candidates {
			if ctx.Err() != nil {
				break
			}
			checked++
			check(stream)
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return
}

func (c *GQLClient) channelHasCampaign(ctx context.Context, channelID, campaignID string) (bool, error) {
	var data struct {
		Channel *struct{ ViewerDropCampaigns []struct{ ID string } }
	}
	if err := c.campaignQuery(ctx, "DropsHighlightService_AvailableDrops", "782dad0f032942260171d2d80a654f88bdd0c5a9dddc392e9bc92218a0f42d20",
		map[string]any{"channelID": channelID}, &data); err != nil {
		return false, err
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
