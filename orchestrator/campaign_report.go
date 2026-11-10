package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *scheduler) campaignReport(g *gameState) string {
	var items []*campaign
	for _, c := range g.Campaigns {
		if c.Finished != "" || !time.Now().Before(c.Ends) {
			items = append(items, c)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ends.After(items[j].Ends) })
	if len(items) == 0 {
		return "Завершённых кампаний пока нет."
	}
	var out strings.Builder
	for i, c := range items {
		if i > 0 {
			out.WriteByte('\n')
		}
		counts := make(map[string]int)
		for _, result := range c.Results {
			counts[result]++
		}
		status := c.Finished
		if status == "" {
			status = "срок истёк"
		}
		fmt.Fprintf(&out, "%s · %s · до %s\n", c.Name, status, c.Ends.Local().Format("02.01 15:04 MST"))
		fmt.Fprintf(&out, "Готово: %d · ошибки входа: %d\nСняты вручную: %d · остановлены по сроку: %d\n", counts["idle"], counts["error: auth"], counts["trimmed"], counts["stopped"])
		if c.CompletedGroups > 0 {
			fmt.Fprintf(&out, "Групп с результатом: %d · суммарно прогноз %s, факт %s, разница %s\n", c.CompletedGroups, c.PlannedGroupTime.Round(time.Minute), c.ActualGroupTime.Round(time.Minute), (c.ActualGroupTime - c.PlannedGroupTime).Round(time.Minute))
		} else {
			fmt.Fprintln(&out, "Замеров по группам пока нет.")
		}
	}
	return out.String()
}
