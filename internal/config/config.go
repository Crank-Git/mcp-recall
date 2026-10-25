// Package config loads mcp-recall's TOML configuration, merging user overrides
// over built-in defaults. Mirrors the behaviour of the original config.ts:
// a missing file is silent, an invalid file warns and falls back to full
// defaults, and present keys override defaults field-by-field.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"mcprecall/internal/logx"
)

// Store holds the [store] section.
type Store struct {
	ExpireAfterSessionDays     int
	Key                        string // "git_root" | "cwd"
	MaxSizeMB                  float64
	MaxPinnedMB                float64
	PinRecommendationThreshold int
	StaleItemDays              int
	EvictionHalfLifeDays       int
	GCReminderMB               float64
	// Retention decides which intercepted outputs keep a retrievable full body:
	// "full" keeps every body; "balanced" keeps MCP/web/API results, network
	// Bash and content reads (cat, git diff/show/log -p, head/tail, sed -n), and
	// drops other reproducible Bash (git log/tests/ls/grep…); "minimal" drops
	// all intercepted bodies. Notes always keep their body regardless.
	Retention string
}

// defaultPinnedFraction is the share of the effective total cap that bounds
// pinned data when max_pinned_mb is not set explicitly. Deriving it (rather than
// using a fixed default) means lowering max_size_mb alone can never manufacture a
// max_pinned_mb > max_size_mb contradiction.
const defaultPinnedFraction = 0.5

// tomlNum decodes a TOML number that may be written as an integer or a float
// (max_size_mb allows fractional MB, e.g. 0.003 for a 3KB cap).
type tomlNum float64

func (n *tomlNum) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case int64:
		*n = tomlNum(x)
	case float64:
		*n = tomlNum(x)
	}
	return nil
}

// Retrieve holds the [retrieve] section.
type Retrieve struct {
	DefaultMaxBytes int
}

// Denylist holds the [denylist] section.
type Denylist struct {
	Additional       []string
	OverrideDefaults []string
	Allowlist        []string
}

// Profiles holds the [profiles] section.
type Profiles struct {
	VerifySignature string // "warn" | "error" | "skip"
}

// Debug holds the [debug] section.
type Debug struct {
	Enabled bool
}

// Config is the fully-resolved configuration.
type Config struct {
	Store    Store
	Retrieve Retrieve
	Denylist Denylist
	Profiles Profiles
	Debug    Debug
}

func defaults() Config {
	return Config{
		Store: Store{
			ExpireAfterSessionDays:     30,
			Key:                        "git_root",
			MaxSizeMB:                  500,
			MaxPinnedMB:                250,
			PinRecommendationThreshold: 5,
			StaleItemDays:              3,
			EvictionHalfLifeDays:       7,
			GCReminderMB:               2048,
			Retention:                  "balanced",
		},
		Retrieve: Retrieve{DefaultMaxBytes: 8192},
		Denylist: Denylist{Additional: []string{}, OverrideDefaults: []string{}, Allowlist: []string{}},
		Profiles: Profiles{VerifySignature: "warn"},
		Debug:    Debug{Enabled: false},
	}
}

// ── Partial (pointer) shapes for field-by-field override ──────────────────────

type partial struct {
	Store    *partialStore    `toml:"store"`
	Retrieve *partialRetrieve `toml:"retrieve"`
	Denylist *partialDenylist `toml:"denylist"`
	Profiles *partialProfiles `toml:"profiles"`
	Debug    *partialDebug    `toml:"debug"`
}

type partialStore struct {
	ExpireAfterSessionDays     *int     `toml:"expire_after_session_days"`
	Key                        *string  `toml:"key"`
	MaxSizeMB                  *tomlNum `toml:"max_size_mb"`
	MaxPinnedMB                *tomlNum `toml:"max_pinned_mb"`
	PinRecommendationThreshold *int     `toml:"pin_recommendation_threshold"`
	StaleItemDays              *int     `toml:"stale_item_days"`
	EvictionHalfLifeDays       *int     `toml:"eviction_half_life_days"`
	GCReminderMB               *tomlNum `toml:"gc_reminder_mb"`
	Retention                  *string  `toml:"retention"`
}

type partialRetrieve struct {
	DefaultMaxBytes *int `toml:"default_max_bytes"`
}

type partialDenylist struct {
	Additional       *[]string `toml:"additional"`
	OverrideDefaults *[]string `toml:"override_defaults"`
	Allowlist        *[]string `toml:"allowlist"`
}

type partialProfiles struct {
	VerifySignature *string `toml:"verify_signature"`
}

type partialDebug struct {
	Enabled *bool `toml:"enabled"`
}

var cached *Config

func configPath() string {
	if p := os.Getenv("RECALL_CONFIG_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mcp-recall", "config.toml")
}

// Load returns the resolved configuration, cached for the process lifetime.
func Load() Config {
	if cached != nil {
		return *cached
	}
	cfg := defaults()

	raw, err := os.ReadFile(configPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logx.Warn(fmt.Sprintf("failed to load config: %v; using defaults", err))
		}
		cached = &cfg
		logx.SetDebugEnabled(cfg.Debug.Enabled)
		return cfg
	}

	var p partial
	if _, err := toml.Decode(string(raw), &p); err != nil {
		logx.Warn(fmt.Sprintf("invalid config (%v); using defaults", err))
		cached = &cfg
		logx.SetDebugEnabled(cfg.Debug.Enabled)
		return cfg
	}

	if issues := validate(&p); len(issues) > 0 {
		logx.Warn(fmt.Sprintf("invalid config (%s); using defaults", issues))
		cached = &cfg
		logx.SetDebugEnabled(cfg.Debug.Enabled)
		return cfg
	}

	merge(&cfg, &p)

	// Cross-field guard: a pinned cap above the total cap is a contradiction
	// (max_pinned_mb can never bind before max_size_mb does). Only reachable when
	// the user set both explicitly. Reject the whole config to defaults, consistent
	// with how a schema-invalid value is handled. This lives here rather than in
	// validate() because that inspects the partial, where one or both fields may
	// be absent and the effective values are not yet known.
	if cfg.Store.MaxPinnedMB > cfg.Store.MaxSizeMB {
		logx.Warn(fmt.Sprintf("invalid config (store.max_pinned_mb %g exceeds store.max_size_mb %g); using defaults",
			cfg.Store.MaxPinnedMB, cfg.Store.MaxSizeMB))
		cfg = defaults()
	}

	cached = &cfg
	logx.SetDebugEnabled(cfg.Debug.Enabled)
	return cfg
}

// Reset clears the cache. Used by tests.
func Reset() {
	cached = nil
	logx.SetDebugEnabled(false)
}

func validate(p *partial) string {
	var issues []string
	if p.Store != nil {
		s := p.Store
		if s.ExpireAfterSessionDays != nil && *s.ExpireAfterSessionDays <= 0 {
			issues = append(issues, "store.expire_after_session_days: must be positive")
		}
		if s.Key != nil && *s.Key != "git_root" && *s.Key != "cwd" {
			issues = append(issues, "store.key: must be git_root or cwd")
		}
		if s.MaxSizeMB != nil && !(float64(*s.MaxSizeMB) > 0) {
			issues = append(issues, "store.max_size_mb: must be positive")
		}
		if s.MaxPinnedMB != nil && !(float64(*s.MaxPinnedMB) > 0) {
			issues = append(issues, "store.max_pinned_mb: must be positive")
		}
		if s.PinRecommendationThreshold != nil && *s.PinRecommendationThreshold <= 0 {
			issues = append(issues, "store.pin_recommendation_threshold: must be positive")
		}
		if s.StaleItemDays != nil && *s.StaleItemDays <= 0 {
			issues = append(issues, "store.stale_item_days: must be positive")
		}
		if s.EvictionHalfLifeDays != nil && *s.EvictionHalfLifeDays <= 0 {
			issues = append(issues, "store.eviction_half_life_days: must be positive")
		}
		// Non-negative, not positive: 0 is the documented way to disable the reminder.
		// Negated comparisons so NaN fails too (TOML accepts `nan`).
		if s.GCReminderMB != nil && !(float64(*s.GCReminderMB) >= 0) {
			issues = append(issues, "store.gc_reminder_mb: must not be negative")
		}
		if s.Retention != nil {
			switch *s.Retention {
			case "full", "balanced", "minimal":
			default:
				issues = append(issues, "store.retention: must be full, balanced, or minimal")
			}
		}
	}
	if p.Retrieve != nil && p.Retrieve.DefaultMaxBytes != nil && *p.Retrieve.DefaultMaxBytes <= 0 {
		issues = append(issues, "retrieve.default_max_bytes: must be positive")
	}
	if p.Profiles != nil && p.Profiles.VerifySignature != nil {
		v := *p.Profiles.VerifySignature
		if v != "warn" && v != "error" && v != "skip" {
			issues = append(issues, "profiles.verify_signature: must be warn, error, or skip")
		}
	}
	if len(issues) == 0 {
		return ""
	}
	out := issues[0]
	for _, i := range issues[1:] {
		out += ", " + i
	}
	return out
}

func merge(c *Config, p *partial) {
	if p.Store != nil {
		s := p.Store
		if s.ExpireAfterSessionDays != nil {
			c.Store.ExpireAfterSessionDays = *s.ExpireAfterSessionDays
		}
		if s.Key != nil {
			c.Store.Key = *s.Key
		}
		if s.MaxSizeMB != nil {
			c.Store.MaxSizeMB = float64(*s.MaxSizeMB)
		}
		// Derive the pinned cap from the effective total cap unless set explicitly,
		// so a user who only lowers max_size_mb doesn't trip the contradiction guard.
		if s.MaxPinnedMB != nil {
			c.Store.MaxPinnedMB = float64(*s.MaxPinnedMB)
		} else {
			c.Store.MaxPinnedMB = c.Store.MaxSizeMB * defaultPinnedFraction
		}
		if s.PinRecommendationThreshold != nil {
			c.Store.PinRecommendationThreshold = *s.PinRecommendationThreshold
		}
		if s.StaleItemDays != nil {
			c.Store.StaleItemDays = *s.StaleItemDays
		}
		if s.EvictionHalfLifeDays != nil {
			c.Store.EvictionHalfLifeDays = *s.EvictionHalfLifeDays
		}
		if s.GCReminderMB != nil {
			c.Store.GCReminderMB = float64(*s.GCReminderMB)
		}
		if s.Retention != nil {
			c.Store.Retention = *s.Retention
		}
	}
	if p.Retrieve != nil && p.Retrieve.DefaultMaxBytes != nil {
		c.Retrieve.DefaultMaxBytes = *p.Retrieve.DefaultMaxBytes
	}
	if p.Denylist != nil {
		if p.Denylist.Additional != nil {
			c.Denylist.Additional = *p.Denylist.Additional
		}
		if p.Denylist.OverrideDefaults != nil {
			c.Denylist.OverrideDefaults = *p.Denylist.OverrideDefaults
		}
		if p.Denylist.Allowlist != nil {
			c.Denylist.Allowlist = *p.Denylist.Allowlist
		}
	}
	if p.Profiles != nil && p.Profiles.VerifySignature != nil {
		c.Profiles.VerifySignature = *p.Profiles.VerifySignature
	}
	if p.Debug != nil && p.Debug.Enabled != nil {
		c.Debug.Enabled = *p.Debug.Enabled
	}
}
