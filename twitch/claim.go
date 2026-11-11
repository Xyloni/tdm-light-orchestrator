package twitch

import (
	"context"
	"fmt"
)

func (c *GQLClient) ClaimDrop(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("Twitch ещё не выдал ID готовой награды")
	}
	const operation = "DropsPage_ClaimDropRewards"
	var data struct {
		ClaimDropRewards *struct{ Status string }
	}
	vars := map[string]any{"input": map[string]string{"dropInstanceID": instanceID}}
	if err := c.campaignQuery(ctx, operation, "2f884fa187b8fadb2a49db0adc033e636f7b6aaee6e76de1e2bba9a7baf0daf6", vars, &data); err != nil {
		return err
	}
	if data.ClaimDropRewards == nil {
		return fmt.Errorf("Twitch не вернул результат получения")
	}
	switch data.ClaimDropRewards.Status {
	case "ELIGIBLE_FOR_ALL", "DROP_INSTANCE_ALREADY_CLAIMED":
		return nil
	default:
		return fmt.Errorf("Twitch не подтвердил получение (%q)", data.ClaimDropRewards.Status)
	}
}
