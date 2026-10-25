// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/profiles/resolve.go

package profiles

import (
	"math"
	"strings"

	"mcprecall/internal/handlers"
	"mcprecall/internal/logx"
)

var tierOrder = []Tier{TierUser, TierCommunity, TierBundled}

func tierIndex(t Tier) int {
	for i, x := range tierOrder {
		if x == t {
			return i
		}
	}
	return len(tierOrder)
}

func matchesPattern(toolName, pattern string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(toolName, pattern[:len(pattern)-1])
	}
	return toolName == pattern
}

// patternSpecificity: exact match → +Inf, wildcard → prefix length.
func patternSpecificity(pattern string) float64 {
	if strings.HasSuffix(pattern, "*") {
		return float64(len(pattern) - 1)
	}
	return math.Inf(1)
}

func profileSpecificity(p Loaded, toolName string) float64 {
	best := math.Inf(-1)
	for _, pat := range p.Patterns {
		if matchesPattern(toolName, pat) {
			if s := patternSpecificity(pat); s > best {
				best = s
			}
		}
	}
	return best
}

func tierAllowed(t Tier, tiers []Tier) bool {
	for _, x := range tiers {
		if x == t {
			return true
		}
	}
	return false
}

// resolveProfile picks the best matching profile for a tool name across the
// allowed tiers: user > community > bundled, then higher pattern specificity.
func resolveProfile(toolName string, profiles []Loaded, tiers []Tier) *Loaded {
	var best *Loaded
	for i := range profiles {
		p := &profiles[i]
		if !tierAllowed(p.Tier, tiers) {
			continue
		}
		matched := false
		for _, pat := range p.Patterns {
			if matchesPattern(toolName, pat) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if best == nil {
			best = p
			continue
		}
		bi, ci := tierIndex(best.Tier), tierIndex(p.Tier)
		if ci != bi {
			if ci < bi {
				best = p
			}
			continue
		}
		if profileSpecificity(*p, toolName) > profileSpecificity(*best, toolName) {
			best = p
		}
	}
	return best
}

func makeHandler(p Loaded) handlers.Handler {
	strategy := p.Spec.Strategy
	return func(toolName string, output any) handlers.Result {
		return apply(strategy, output)
	}
}

// LookupHandler returns a compression handler for a tool if a profile matches
// the given tiers, or nil.
func LookupHandler(toolName string, tierStrs []string) handlers.Handler {
	h, _ := lookupNamedHandler(toolName, tierStrs)
	return h
}

// lookupNamedHandler is LookupHandler plus the handler's name,
// `profile:<id>`, as upstream names a profile handler (#341).
func lookupNamedHandler(toolName string, tierStrs []string) (handlers.Handler, string) {
	tiers := make([]Tier, len(tierStrs))
	for i, s := range tierStrs {
		tiers[i] = Tier(s)
	}
	match := resolveProfile(toolName, Load(), tiers)
	if match == nil {
		return nil, ""
	}
	logx.Debug("profile match · " + match.Spec.Profile.ID + " (" + string(match.Tier) + ") · " + toolName)
	return makeHandler(*match), "profile:" + match.Spec.Profile.ID
}

// ResolveAllTiers resolves the best profile for a tool across all tiers from a
// preloaded profile list. Exported for the learn/retrain package.
func ResolveAllTiers(toolName string, loaded []Loaded) *Loaded {
	return resolveProfile(toolName, loaded, tierOrder)
}

// Register wires the profile resolver into the handler dispatch. Call once at
// startup so PostToolUse and the MCP server both apply profiles.
func Register() {
	handlers.ProfileLookup = lookupNamedHandler
}
