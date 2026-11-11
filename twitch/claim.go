package twitch

import (
	"context"
	"fmt"
)

func (c *GQLClient) claimDrop(ctx context.Context, instanceID string) (claimResponse, error) {
	if instanceID == "" {
		return claimResponse{}, fmt.Errorf("Twitch ещё не выдал ID готовой награды")
	}
	const operation = "DropsPage_ClaimDropRewards"
	var data struct {
		ClaimDropRewards *claimResponse
	}
	vars := map[string]any{"input": map[string]string{"dropInstanceID": instanceID}}
	if err := c.campaignQuery(ctx, operation, "2f884fa187b8fadb2a49db0adc033e636f7b6aaee6e76de1e2bba9a7baf0daf6", vars, &data); err != nil {
		return claimResponse{}, fmt.Errorf("запрос получения не удался: %w", err)
	}
	if data.ClaimDropRewards == nil {
		return claimResponse{}, fmt.Errorf("Twitch не вернул результат получения")
	}
	return *data.ClaimDropRewards, nil
}

type claimResponse struct {
	Status                 string
	IsUserAccountConnected *bool
}

func (r claimResponse) description() string {
	connection := "привязка не указана"
	if r.IsUserAccountConnected != nil {
		connection = "аккаунт не привязан"
		if *r.IsUserAccountConnected {
			connection = "аккаунт привязан"
		}
	}
	return fmt.Sprintf("статус Twitch %q, %s", r.Status, connection)
}

func (c *GQLClient) ClaimAndVerifyDrop(ctx context.Context, drop Drop) (Drop, error) {
	result, err := c.claimDrop(ctx, drop.DropInstanceID)
	if err != nil {
		return Drop{}, err
	}
	switch result.Status {
	case "ELIGIBLE_FOR_ALL", "DROP_INSTANCE_ALREADY_CLAIMED":
	default:
		return Drop{}, fmt.Errorf("Twitch не подтвердил получение (%s)", result.description())
	}
	drops, err := c.DropsDashboard(ctx)
	if err != nil {
		return Drop{}, fmt.Errorf("не удалось проверить инвентарь после запроса (%s): %w", result.description(), err)
	}
	for _, item := range drops {
		if item.ID == drop.ID && item.CampaignID == drop.CampaignID && item.IsClaimed {
			return item, nil
		}
	}
	return Drop{}, fmt.Errorf("в инвентаре пока нет отметки о получении (%s)", result.description())
}
