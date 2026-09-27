package tool

import "testing"

func TestHasProgress(t *testing.T) {
	if (&Env{}).complete().HasProgress() {
		t.Error("nil Progress: HasProgress after complete = true")
	}
	e := &Env{Progress: func(float64, float64, string) {}}
	if !e.complete().HasProgress() {
		t.Error("client Progress: HasProgress after complete = false")
	}
	// A completed Env passed through complete again stays progress-less.
	if (&Env{}).complete().complete().HasProgress() {
		t.Error("re-completed Env reports progress")
	}
}
