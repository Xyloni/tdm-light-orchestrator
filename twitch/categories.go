package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

func (c *GQLClient) SearchCategories(ctx context.Context, query string) ([]string, error) {
	params := url.Values{"query": {query}, "first": {"15"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.twitch.tv/helix/search/categories?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-Id", c.cfg.ClientID)
	req.Header.Set("Authorization", "Bearer "+c.Account.OAuth)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Twitch category search: HTTP %d", resp.StatusCode)
	}
	var result struct{ Data []struct{ Name string } }
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(result.Data))
	for _, category := range result.Data {
		if category.Name != "" {
			names = append(names, category.Name)
		}
	}
	return names, nil
}
