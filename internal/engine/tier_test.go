package engine

import "testing"

func TestAutoEffortFollowsTheSizeOfThePullRequest(t *testing.T) {
	s := Spec{Name: "claude", Model: "sonnet"}
	levels := []string{"low", "medium", "high", "xhigh", "max"}
	for tier, want := range map[Tier]string{Light: "low", Medium: "medium", Heavy: "high"} {
		if got := s.ForTier(tier, levels).Effort; got != want {
			t.Errorf("%s: effort = %q, want %q", tier, got, want)
		}
	}
}

func TestAutoEffortUsesTheClosestLevelTheModelHas(t *testing.T) {
	// deepseek-v4.1-flash-free offers only low, high and max.
	s := Spec{Name: "opencode"}
	levels := []string{"low", "high", "max"}
	if got := s.ForTier(Heavy, levels).Effort; got != "high" {
		t.Errorf("heavy = %q", got)
	}
	if got := s.ForTier(Medium, levels).Effort; got != "low" {
		t.Errorf("medium has no exact level: the lower neighbour (faster) wins a tie, got %q", got)
	}
	if got := s.ForTier(Heavy, nil).Effort; got != "" {
		t.Errorf("an engine with no levels gets none, got %q", got)
	}
}

func TestTheEffortIsAlwaysResolvedFromTheSizeNeverFromConfig(t *testing.T) {
	s := Spec{Name: "claude", Effort: "xhigh"} // a stale value from an old config
	if got := s.ForTier(Light, []string{"low", "xhigh"}).Effort; got != "low" {
		t.Fatalf("got %q", got)
	}
}

func TestAModelNamedForTheSizeWinsOverTheDefaultModel(t *testing.T) {
	s := Spec{Name: "claude", Model: "sonnet", Tiers: TierModels{Light: "haiku", Heavy: "opus"}}
	if s.ForTier(Light, nil).Model != "haiku" || s.ForTier(Heavy, nil).Model != "opus" || s.ForTier(Medium, nil).Model != "sonnet" {
		t.Fatalf("light=%s medium=%s heavy=%s", s.ForTier(Light, nil).Model, s.ForTier(Medium, nil).Model, s.ForTier(Heavy, nil).Model)
	}
}
