package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

func accountExportName(pool string, campaigns ...string) string {
	safePart := func(value string, limit int) string {
		var result strings.Builder
		for _, r := range strings.Join(strings.Fields(value), " ") {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`<>:"/\|?*`, r) {
				r = '-'
			}
			if result.Len()+len(string(r)) > limit {
				break
			}
			result.WriteRune(r)
		}
		return strings.Trim(result.String(), " .-")
	}
	name := "Аккаунты"
	if part := safePart(pool, 60); part != "" {
		name += " — " + part
	}
	labels := append([]string(nil), campaigns...)
	sort.Strings(labels)
	for _, label := range labels[:min(3, len(labels))] {
		if part := safePart(label, 43); part != "" {
			name += " — " + part
		}
	}
	if len(labels) > 3 {
		name += fmt.Sprintf(" +%d", len(labels)-3)
	}
	return name + ".txt"
}
