//go:build !linux

package orchestrator

import "time"

type resourceReader struct{}

func (*resourceReader) sample(_ string, now time.Time) resourceSample {
	return resourceSample{at: now}
}
