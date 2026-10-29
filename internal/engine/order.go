package engine

import (
	"math"
	"sort"
	"strings"
)

// RecommendedGroup is the section of recommended paid models; RecommendedFreeGroup, at the very top,
// holds the free ones.
const (
	RecommendedGroup     = "Recommended"
	RecommendedFreeGroup = "Recommended · free"
)

var tierRank = map[Tier]int{Light: 0, Medium: 1, Heavy: 2}

// priceRank sorts cheapest first: free models, then by what a million tokens in and out cost, and
// models whose price the catalog does not give last.
func priceRank(m Model) float64 {
	switch {
	case m.Free:
		return -1
	case m.PriceKnown:
		return m.InUSD + m.OutUSD
	}
	return math.Inf(1)
}

// OrderModels puts the models in the order a person should read them: the recommended ones first,
// from light to heavy reviews, then every brand in alphabetical order with its models cheapest
// first. Each model's Group becomes its brand (or "Recommended"), so the picker shows brand
// sections and no section appears twice.
func OrderModels(engineName string, models []Model) []Model {
	out := make([]Model, len(models))
	copy(out, models)
	spelling := map[string]string{} // brands that differ only in case share the first spelling seen
	for i := range out {
		m := &out[i]
		if brand := BrandOf(engineName, m.ID); brand != "" {
			m.Group = brand
		}
		key := strings.ToLower(m.Group)
		if _, seen := spelling[key]; !seen {
			spelling[key] = m.Group
		}
		m.Group = spelling[key]
		m.Brand = m.Group
		if tier, ok := RecommendedTier(engineName, m.ID); ok {
			m.Tier = tier
			m.Group = RecommendedGroup
			if RecommendedFree(engineName, m.ID) {
				m.Group = RecommendedFreeGroup
			}
			if m.Note != "" {
				m.Note = tierDescription[tier] + ". " + m.Note // keep what the catalog says, such as the access limits
			} else {
				m.Note = tierDescription[tier]
			}
			tag := m.Group + " · " + string(tier)
			if m.Tag != "" {
				tag += " · " + m.Tag
			}
			m.Tag = tag
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Tier != "") != (b.Tier != "") {
			return a.Tier != ""
		}
		if a.Tier != "" {
			if fa, fb := a.Group == RecommendedFreeGroup, b.Group == RecommendedFreeGroup; fa != fb {
				return fa // free picks first
			}
			return tierRank[a.Tier] < tierRank[b.Tier]
		}
		if ba, bb := strings.ToLower(a.Brand), strings.ToLower(b.Brand); ba != bb {
			return ba < bb
		}
		if pa, pb := priceRank(a), priceRank(b); pa != pb {
			return pa < pb
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out
}
