package orchestrator

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"
)

type coverageCampaign struct {
	ID, Name                          string
	Complete, Ready, Claimed, Unknown int
}

type coverageSet struct {
	IDs   []string
	Names string
	Count int
}

type campaignCoverage struct {
	Campaigns []coverageCampaign
	Sets      []coverageSet
	Accounts  []account
	completed [][]string
}

// coverage describes the current pool using locally confirmed results.
// The caller holds the scheduler lock.
func (s *scheduler) coverage(key string, g *gameState) campaignCoverage {
	var out campaignCoverage
	for _, a := range s.state.Accounts {
		if a.Game == key {
			out.Accounts = append(out.Accounts, a)
		}
	}
	out.completed = make([][]string, len(out.Accounts))
	cfg := s.configForGame(g)
	campaigns := make([]*campaign, 0, len(g.Campaigns))
	for _, c := range g.Campaigns {
		campaigns = append(campaigns, c)
	}
	sort.Slice(campaigns, func(i, j int) bool {
		a, b := campaigns[i], campaigns[j]
		if !a.Starts.Equal(b.Starts) {
			return a.Starts.After(b.Starts)
		}
		return a.ID < b.ID
	})
	for _, c := range campaigns {
		name := c.Name
		if name == "" {
			name = c.ID
		}
		line := coverageCampaign{ID: c.ID, Name: name}
		var rewards []rewardRequirement
		for _, reward := range c.Rewards {
			if reward.Minutes > 0 && !cfg.DropIgnored(reward.Name) {
				rewards = append(rewards, reward)
			}
		}
		for i, a := range out.Accounts {
			earned := len(rewards) > 0 && a.Credentials.UserID != ""
			claimed := earned
			for _, reward := range rewards {
				id := reward.ID
				progress := s.state.Stats.Progress[key+"\x00"+c.ID+"\x00"+a.Credentials.UserID+"\x00"+id]
				earned = earned && id != "" && (progress.Earned || progress.Ready || progress.Claimed ||
					(progress.Known && progress.Minutes >= reward.Minutes))
				claimed = claimed && id != "" && progress.Claimed
			}
			if !earned && c.Results[a.Credentials.Login] != "idle" {
				continue
			}
			line.Complete++
			switch {
			case claimed:
				line.Claimed++
			case earned:
				line.Ready++
			default:
				line.Unknown++
			}
			out.completed[i] = append(out.completed[i], c.ID)
		}
		out.Campaigns = append(out.Campaigns, line)
	}
	names := make(map[string]string, len(out.Campaigns))
	for _, c := range out.Campaigns {
		names[c.ID] = c.Name
	}
	bySet := make(map[string]int)
	for _, ids := range out.completed {
		k := strings.Join(ids, "\x00")
		if i, ok := bySet[k]; ok {
			out.Sets[i].Count++
			continue
		}
		labels := make([]string, 0, len(ids))
		for _, id := range ids {
			labels = append(labels, names[id])
		}
		label := strings.Join(labels, " + ")
		if label == "" {
			label = "Нет подтверждённых кампаний"
		}
		bySet[k] = len(out.Sets)
		out.Sets = append(out.Sets, coverageSet{IDs: ids, Names: label, Count: 1})
	}
	sort.SliceStable(out.Sets, func(i, j int) bool {
		if out.Sets[i].Count != out.Sets[j].Count {
			return out.Sets[i].Count > out.Sets[j].Count
		}
		return out.Sets[i].Names < out.Sets[j].Names
	})
	return out
}

func (c campaignCoverage) selectAccounts(selected []string, mode string) ([]account, error) {
	if len(selected) == 0 || (mode != "all" && mode != "any" && mode != "exact") {
		return nil, fmt.Errorf("выберите кампании и условие выгрузки")
	}
	ids := make(map[string]bool, len(selected))
	for _, id := range selected {
		if !slices.ContainsFunc(c.Campaigns, func(item coverageCampaign) bool { return item.ID == id }) {
			return nil, fmt.Errorf("одна из выбранных кампаний больше недоступна")
		}
		ids[id] = true
	}
	var result []account
	for i, completed := range c.completed {
		matches := 0
		for _, id := range completed {
			if ids[id] {
				matches++
			}
		}
		if mode == "any" && matches > 0 || mode == "all" && matches == len(ids) ||
			mode == "exact" && matches == len(ids) && len(completed) == len(ids) {
			result = append(result, c.Accounts[i])
		}
	}
	return result, nil
}

func campaignExportModeLabel(mode string) string {
	switch mode {
	case "all":
		return "Все выбранные"
	case "any":
		return "Хотя бы одна"
	case "exact":
		return "Только этот набор"
	}
	return ""
}

type accountExportFormat string

const (
	exportFull          accountExportFormat = "full"
	exportLoginPassword accountExportFormat = "login-password"
)

func encodeAccountFile(accounts []account, format accountExportFormat) ([]byte, error) {
	var content bytes.Buffer
	for _, a := range accounts {
		acc := a.Credentials
		fields := []string{acc.Email, acc.Login, acc.Password, acc.OAuth, acc.UserID, acc.DeviceID}
		if format == exportLoginPassword {
			fields = fields[1:3]
		}
		for _, field := range fields {
			if strings.ContainsAny(field, ":\r\n") {
				return nil, fmt.Errorf("в пуле есть запись, которую нельзя сохранить в выбранном формате")
			}
		}
		content.WriteString(strings.Join(fields, ":"))
		content.WriteByte('\n')
		if content.Len() > 2<<20 {
			return nil, fmt.Errorf("файл превышает предел обратной загрузки 2 МБ")
		}
	}
	return content.Bytes(), nil
}

func campaignExportFingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum[:8])
}
