package worker

import (
	"testing"
	"time"
)

func TestSustainedRateModerateLossAndRecoveredViewing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		percent   int
		recovered bool
		want      bool
	}{
		{"75 percent", 75, false, true}, {"85 percent", 85, false, true},
		{"90 percent", 90, false, false}, {"normal", 100, false, false},
		{"catch up", 105, false, false}, {"recovered after early outage", 75, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := time.Now()
			var short, long watchRate
			short.reset(base, 100, true)
			long.reset(base, 100, true)
			var detected bool
			for i := 2; i <= 30; i += 2 {
				gain := i * tc.percent / 100
				if tc.recovered {
					if i <= 10 {
						gain = 0
					} else {
						gain = i - 10
					}
				}
				_, recent, window := short.observe(base.Add(time.Duration(i)*time.Minute), 100+gain, true, 5*time.Minute)
				slow, _, _ := long.observeSustained(base.Add(time.Duration(i)*time.Minute), 100+gain, true, 5*time.Minute, recent, window)
				if i < 30 && slow {
					t.Fatal("long recovery before full window")
				}
				detected = slow
			}
			if detected != tc.want {
				t.Fatalf("detected=%v want=%v", detected, tc.want)
			}
		})
	}
}

func TestSustainedRateInvalidDataAndBoundedHistory(t *testing.T) {
	base := time.Now()
	for _, kind := range []string{"unknown", "gap", "regression", "clock"} {
		t.Run(kind, func(t *testing.T) {
			var r watchRate
			r.reset(base, 100, true)
			for i := 2; i < 30; i += 2 {
				r.observeSustained(base.Add(time.Duration(i)*time.Minute), 100+i*3/4, true, 5*time.Minute, 7, 10*time.Minute)
			}
			at, minutes, known := base.Add(30*time.Minute), 122, true
			switch kind {
			case "unknown":
				known = false
			case "gap":
				at = base.Add(40 * time.Minute)
			case "regression":
				minutes = 10
			case "clock":
				at = base
			}
			slow, _, _ := r.observeSustained(at, minutes, known, 5*time.Minute, 7, 10*time.Minute)
			if slow || len(r.samples) > 1 {
				t.Fatal("invalid data retained long window")
			}
		})
	}
	var r watchRate
	for i := 0; i < 10000; i++ {
		r.observeSustained(base.Add(time.Duration(i)*2*time.Minute), i*2, true, 5*time.Minute, 10, 10*time.Minute)
		if len(r.samples) > 16 {
			t.Fatal("history grows without bound")
		}
	}
}
