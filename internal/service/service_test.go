package service

import "testing"

func TestRestartIsNeededOnlyWhenTheServiceIsNotRunningOrItsDefinitionChanged(t *testing.T) {
	same := []byte("<plist>same</plist>")
	cases := []struct {
		name      string
		existing  []byte
		rendered  []byte
		loaded    bool
		wantStart bool
	}{
		{"running with an unchanged definition keeps running, so a review in progress is not killed", same, same, true, false},
		{"not running must be started", same, same, false, true},
		{"running with a changed definition (new binary path or PATH) must be restarted", []byte("<plist>old</plist>"), same, true, true},
		{"first install must be started", nil, same, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsStart(c.existing, c.rendered, c.loaded); got != c.wantStart {
				t.Errorf("needsStart = %v, want %v", got, c.wantStart)
			}
		})
	}
}
