package api

import "testing"

func TestSttTuning(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	n := func(v int) *int { return &v }

	if clampSilence(nil) != 0 || clampSilence(f(0.01)) != 0.2 || clampSilence(f(99)) != 5 || clampSilence(f(0.8)) != 0.8 {
		t.Error("silence is the worker default when absent, else 0.2-5")
	}
	if stableSecondsOr(f(0)) != 0 || stableSecondsOr(f(-1)) != 0 || stableSecondsOr(f(0.1)) != 0.3 || stableSecondsOr(f(30)) != 5 || stableSecondsOr(f(2)) != 2 {
		t.Error("stable seconds: 0 is off, else 0.3-5")
	}
	if clampRepeats(nil) != nil {
		t.Error("repeats absent must stay the worker default")
	}
	if *clampRepeats(n(-3)) != 0 || *clampRepeats(n(5000)) != 1000 || *clampRepeats(n(10)) != 10 {
		t.Error("repeats are 0-1000")
	}
}
