package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type LotInventory struct {
	Drops           []Drop
	ClaimedBenefits map[string]bool
	Empty           bool
}

type lotInventoryCampaign struct {
	ID             string
	TimeBasedDrops []struct {
		ID       string
		Required int `json:"requiredMinutesWatched"`
		Self     *struct {
			Current  *int   `json:"currentMinutesWatched"`
			Claimed  *bool  `json:"isClaimed"`
			Instance string `json:"dropInstanceID"`
		}
	}
}

type LotCampaign struct {
	ID, Name       string
	Game           struct{ Name string }
	TimeBasedDrops []struct {
		ID, Name     string
		BenefitEdges []struct{ Benefit struct{ ID string } }
	}
}

func (c *GQLClient) LotCampaigns(ctx context.Context) ([]LotCampaign, error) {
	const query = `query TDMManagedLotCatalog {
		currentUser { dropCampaigns {
			id name game { name }
			timeBasedDrops { id name benefitEdges { benefit { id } } }
		} }
	}`
	var result struct {
		CurrentUser *struct{ DropCampaigns []LotCampaign }
	}
	if err := c.do(ctx, "TDMManagedLotCatalog", query, nil, &result); err != nil {
		return nil, err
	}
	if result.CurrentUser == nil || result.CurrentUser.DropCampaigns == nil {
		return nil, fmt.Errorf("Twitch did not return a complete catalog")
	}
	return result.CurrentUser.DropCampaigns, nil
}

// ReadLotInventory uses only the authenticated user's inventory. Catalog
// entries do not prove that this user has a reward or can claim it.
func (c *GQLClient) ReadLotInventory(ctx context.Context) (LotInventory, error) {
	const query = `query TDMManagedLotInventory {
		currentUser { id login inventory {
			gameEventDrops { id }
			dropCampaignsInProgress { id timeBasedDrops {
				id requiredMinutesWatched self { currentMinutesWatched isClaimed dropInstanceID }
			} }
		} }
	}`
	var response struct {
		CurrentUser *struct {
			ID, Login string
			Inventory *struct {
				GameEventDrops          []struct{ ID string }
				DropCampaignsInProgress json.RawMessage
			}
		}
	}
	if err := c.do(ctx, "TDMManagedLotInventory", query, nil, &response); err != nil {
		return LotInventory{}, err
	}
	user := response.CurrentUser
	if user == nil || user.ID != c.Account.UserID || !strings.EqualFold(user.Login, c.Account.Login) {
		return LotInventory{}, fmt.Errorf("Twitch did not confirm account identity")
	}
	inv := user.Inventory
	if inv == nil || inv.GameEventDrops == nil || inv.DropCampaignsInProgress == nil {
		return LotInventory{}, fmt.Errorf("Twitch did not return a complete inventory")
	}
	var campaigns []lotInventoryCampaign
	if err := json.Unmarshal(inv.DropCampaignsInProgress, &campaigns); err != nil {
		return LotInventory{}, fmt.Errorf("Twitch returned an invalid inventory list")
	}
	result := LotInventory{Empty: len(inv.GameEventDrops) == 0, ClaimedBenefits: make(map[string]bool)}
	for _, benefit := range inv.GameEventDrops {
		if benefit.ID == "" {
			return LotInventory{}, fmt.Errorf("Twitch returned an incomplete claimed benefit")
		}
		result.ClaimedBenefits[benefit.ID] = true
	}
	for _, campaign := range campaigns {
		if campaign.ID == "" || campaign.TimeBasedDrops == nil {
			return LotInventory{}, fmt.Errorf("Twitch returned an incomplete campaign")
		}
		for _, drop := range campaign.TimeBasedDrops {
			if drop.ID == "" {
				return LotInventory{}, fmt.Errorf("Twitch returned an incomplete reward")
			}
			item := Drop{ID: drop.ID, CampaignID: campaign.ID, Required: drop.Required}
			if progress := drop.Self; progress != nil {
				item.IsClaimed = progress.Claimed != nil && *progress.Claimed
				item.DropInstanceID = progress.Instance
				item.ProgressKnown = progress.Current != nil && progress.Claimed != nil
				if progress.Current != nil {
					item.Current = *progress.Current
				}
			}
			result.Empty = result.Empty && item.ProgressKnown && !item.IsClaimed && item.Current == 0 && item.DropInstanceID == ""
			result.Drops = append(result.Drops, item)
		}
	}
	return result, nil
}
