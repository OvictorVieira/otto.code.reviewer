package boot

import "testing"

func TestDarkFollowsCOLORFGBGAndDefaultsToDark(t *testing.T) {
	for in, want := range map[string]bool{"": true, "15;0": true, "0;15": false, "default;default": true, "7;8": true} {
		if got := dark(in); got != want {
			t.Errorf("dark(%q) = %v, want %v", in, got, want)
		}
	}
}
