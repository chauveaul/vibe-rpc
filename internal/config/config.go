package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/meze/agy-rpc/internal/paths"
)

type SourcesConfig struct {
	Antigravity *bool `json:"antigravity,omitempty"`
	Claude      *bool `json:"claude,omitempty"`
}

func (s SourcesConfig) WantAntigravity() bool {
	return s.Antigravity == nil || *s.Antigravity
}

func (s SourcesConfig) WantClaude() bool {
	return s.Claude == nil || *s.Claude
}

type GoalsConfig struct {
	DailyHours   float64 `json:"dailyHours,omitempty"`
	DailyPrompts int     `json:"dailyPrompts,omitempty"`
	WeeklyHours  float64 `json:"weeklyHours,omitempty"`
}

type BudgetConfig struct {
	Monthly   float64 `json:"monthly,omitempty"`
	WarnAtPct int     `json:"warnAtPct,omitempty"`
}

type WebhookConfig struct {
	URL string   `json:"url,omitempty"`
	On  []string `json:"on,omitempty"`
}

type NotifyConfig struct {
	Enabled        bool  `json:"enabled"`
	OnNotification *bool `json:"onNotification,omitempty"`
}

type TriggerConfig struct {
	Match          string `json:"match"`
	Details        string `json:"details"`
	State          string `json:"state"`
	LargeImageText string `json:"largeImageText,omitempty"`
}

type PrivacyConfig struct {
	Patterns                []string `json:"patterns,omitempty"`
	Mode                    string   `json:"mode,omitempty"`
	AutoDetectGithubPrivate *bool    `json:"autoDetectGithubPrivate,omitempty"`
	GithubPrivateMode       string   `json:"githubPrivateMode,omitempty"`
}

type Frame struct {
	Details        string   `json:"details,omitempty"`
	State          string   `json:"state,omitempty"`
	LargeImageText string   `json:"largeImageText,omitempty"`
	Requires       Requires `json:"requires,omitempty"`
}

// Requires unmarshals either a single JSON string or a JSON array of strings.
type Requires []string

func (r *Requires) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if single != "" {
			*r = []string{single}
		} else {
			*r = nil
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*r = list
	return nil
}

type StatusTemplate struct {
	Details        string  `json:"details,omitempty"`
	State          string  `json:"state,omitempty"`
	LargeImageText string  `json:"largeImageText,omitempty"`
	Rotation       []Frame `json:"rotation,omitempty"`
}

type Button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type PresenceConfig struct {
	LargeImageKey  string                    `json:"largeImageKey,omitempty"`
	LargeImageText string                    `json:"largeImageText,omitempty"`
	SmallImageKey  string                    `json:"smallImageKey,omitempty"`
	SmallImageText string                    `json:"smallImageText,omitempty"`
	ByStatus       map[string]StatusTemplate `json:"byStatus,omitempty"`
	Rotation       []Frame                   `json:"rotation,omitempty"`
	Details        string                    `json:"details,omitempty"`
	State          string                    `json:"state,omitempty"`
	GithubButton   *bool                     `json:"githubButton,omitempty"`
	Buttons        []Button                  `json:"buttons,omitempty"`
}

type Config struct {
	ClientID                string            `json:"clientId"`
	AppName                 string            `json:"appName"`
	Sources                 SourcesConfig     `json:"sources"`
	DynamicAppName          *bool             `json:"dynamicAppName,omitempty"`
	UpdateIntervalMs        int               `json:"updateIntervalMs"`
	RotationIntervalMs      int               `json:"rotationIntervalMs"`
	Autostart               *bool             `json:"autostart,omitempty"`
	MinActivityGapMs        int               `json:"minActivityGapMs"`
	MaxActivityWrites       int               `json:"maxActivityWrites"`
	ActivityWindowMs        int               `json:"activityWindowMs"`
	PresenceKeepaliveSec    *int              `json:"presenceKeepaliveSec,omitempty"`
	RescanIntervalSec       int               `json:"rescanIntervalSec"`
	IdleThresholdSec        int               `json:"idleThresholdSec"`
	StaleSessionMin         int               `json:"staleSessionMin"`
	LiveSessionThresholdSec int               `json:"liveSessionThresholdSec,omitempty"`
	IdleWhenOpen            bool              `json:"idleWhenOpen"`
	ProcessDetection        *bool             `json:"processDetection,omitempty"`
	HideWhenStale           *bool             `json:"hideWhenStale,omitempty"`
	NotificationWindowSec   int               `json:"notificationWindowSec"`
	ShippedFrameSec         int               `json:"shippedFrameSec"`
	Goals                   GoalsConfig       `json:"goals"`
	Budget                  BudgetConfig      `json:"budget"`
	Webhook                 WebhookConfig     `json:"webhook"`
	Notify                  NotifyConfig      `json:"notify"`
	Triggers                []TriggerConfig   `json:"triggers,omitempty"`
	TriggerFrameSec         int               `json:"triggerFrameSec"`
	Privacy                 PrivacyConfig     `json:"privacy"`
	ShowElapsed             *bool             `json:"showElapsed,omitempty"`
	ShowPartySize           *bool             `json:"showPartySize,omitempty"`
	ActivityType            int               `json:"activityType"`
	GifBaseURL              string            `json:"gifBaseUrl,omitempty"`
	StatusAssets            map[string]string `json:"statusAssets"`
	ClaudeStatusAssets      map[string]string `json:"claudeStatusAssets,omitempty"`
	ModelAssets             map[string]string `json:"modelAssets,omitempty"`
	Presence                PresenceConfig    `json:"presence"`
	StatusIcons             map[string]string `json:"statusIcons"`
}

func BoolPtr(v bool) *bool { return &v }

func (c Config) IsAutostart() bool        { return c.Autostart == nil || *c.Autostart }
func (c Config) IsDynamicAppName() bool   { return c.DynamicAppName == nil || *c.DynamicAppName }
func (c Config) IsProcessDetection() bool { return c.ProcessDetection == nil || *c.ProcessDetection }
func (c Config) IsHideWhenStale() bool    { return c.HideWhenStale == nil || *c.HideWhenStale }
func (c Config) IsShowElapsed() bool      { return c.ShowElapsed == nil || *c.ShowElapsed }
func (c Config) IsShowPartySize() bool    { return c.ShowPartySize == nil || *c.ShowPartySize }
func (c Config) IsGithubButton() bool {
	return c.Presence.GithubButton == nil || *c.Presence.GithubButton
}

// Clone returns a deep copy of Config so mutations never alias nested maps or slices.
func (c Config) Clone() Config {
	b, err := json.Marshal(c)
	if err != nil {
		return c
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		return c
	}
	return out
}

const DefaultClientID = "1506443909406920948"

var Statuses = []string{"working", "thinking", "compacting", "shipped", "notification", "idle"}

func Default() Config {
	return Config{
		ClientID:              DefaultClientID,
		AppName:               "Antigravity",
		Sources:               SourcesConfig{Antigravity: BoolPtr(true), Claude: BoolPtr(true)},
		DynamicAppName:        BoolPtr(true),
		UpdateIntervalMs:      4000,
		RotationIntervalMs:    12000,
		Autostart:             BoolPtr(true),
		MinActivityGapMs:      4000,
		MaxActivityWrites:     4,
		ActivityWindowMs:      20000,
		RescanIntervalSec:     300,
		IdleThresholdSec:      60,
		StaleSessionMin:       5,
		IdleWhenOpen:          false,
		ProcessDetection:      BoolPtr(true),
		HideWhenStale:         BoolPtr(true),
		NotificationWindowSec: 8,
		ShippedFrameSec:       60,
		Budget:                BudgetConfig{WarnAtPct: 80},
		Webhook:               WebhookConfig{On: []string{"shipped", "notification"}},
		Notify:                NotifyConfig{Enabled: false, OnNotification: BoolPtr(true)},
		TriggerFrameSec:       20,
		Privacy:               PrivacyConfig{AutoDetectGithubPrivate: BoolPtr(true), GithubPrivateMode: "hidden", Mode: "hidden"},
		ShowElapsed:           BoolPtr(true),
		ShowPartySize:         BoolPtr(true),
		ActivityType:          0,
		StatusAssets: map[string]string{
			"working":      "/assets/gifs/agy-working.gif",
			"thinking":     "/assets/gifs/agy-thinking.gif",
			"compacting":   "/assets/gifs/agy-compacting.gif",
			"shipped":      "/assets/gifs/agy-shipped.gif",
			"idle":         "/assets/gifs/agy-idle.gif",
			"stale":        "/assets/gifs/agy-stale.gif",
			"notification": "/assets/gifs/agy-notification.gif",
		},
		ClaudeStatusAssets: map[string]string{
			"working":      "https://cdn.qualit.ly/clawd-working-building.gif",
			"thinking":     "https://cdn.qualit.ly/clawd-working-typing.gif",
			"compacting":   "https://cdn.qualit.ly/clawd-working-typing.gif",
			"shipped":      "https://cdn.qualit.ly/clawd-working-building.gif",
			"idle":         "https://cdn.qualit.ly/clawd-sleeping.gif",
			"stale":        "https://cdn.qualit.ly/clawd-sleeping.gif",
			"notification": "https://cdn.qualit.ly/clawd-notification.gif",
		},
		Presence: PresenceConfig{
			LargeImageKey:  "/assets/gifs/agy-idle.gif",
			LargeImageText: "{modelPretty} · {allHours} coding · {streakLabel}",
			SmallImageKey:  "{statusIcon}",
			SmallImageText: "{statusVerbose}",
			GithubButton:   BoolPtr(true),
			Buttons:        []Button{},
			ByStatus: map[string]StatusTemplate{
				"working": {
					Details:        "Working in {project}",
					State:          "{currentToolPretty} · {currentFilePretty} · {toolElapsed} · {tokensLabel}",
					LargeImageText: "Working in {project} · {modelPretty}",
					Rotation: []Frame{
						{Details: "{sessionMilestoneLabel} · {project}", State: "{tokensLabel} · {messagesLabel}", Requires: Requires{"sessionMilestoneHit"}},
					},
				},
				"thinking": {
					Details:        "Thinking in {project}",
					State:          "{modelPretty} · {messagesLabel} · {tokensLabel}",
					LargeImageText: "Reasoning with {modelPretty}",
				},
				"compacting": {
					Details:        "Compacting context in {project}",
					State:          "{modelPretty} · {messagesLabel}",
					LargeImageText: "Compacting · {compactTriggerLabel}",
				},
				"shipped": {
					Details:        "{justShippedLabel} · {project}",
					State:          "{lastCommit}",
					LargeImageText: "{justShippedLabel}",
				},
				"notification": {
					Details:        "Waiting on you · {project}",
					State:          "{modelPretty} · {messagesLabel}",
					LargeImageText: "Input needed",
				},
				"idle": {
					Details:        "Idle in {project}",
					State:          "{modelPretty} · {todayHours} today",
					LargeImageText: "Idle · {modelPretty}",
					Rotation: []Frame{
						{Details: "This week · {weekHours}", State: "{weekPromptsLabel} · {weekTokensFmt} tokens", Requires: Requires{"weekActiveMs"}},
						{Details: "{streakLabel}", State: "{daysSinceFirstLabel} · {allSessionsLabel}", Requires: Requires{"streakIsMilestone"}},
						{Details: "Hotspot · {topEditedFile}", State: "{topEditedCountLabel} · {topEditedAgeLabel}", Requires: Requires{"topEditedCount"}},
						{Details: "Model split", State: "{modelSplitLabel}", Requires: Requires{"modelSplitLabel"}},
						{Details: "{allHours} coding all-time", State: "{allSessionsLabel} · {allMessagesFmt} prompts", Requires: Requires{"allSessions"}},
						{Details: "Lifetime · {allTokensFmt} tokens", State: "{allToolsFmt} tool calls · {allFilesFmt} files", Requires: Requires{"allTools"}},
						{Details: "{allFreshTokensFmt} fresh tokens", State: "{allCachePctLabel}", Requires: Requires{"allCachePctLabel"}},
						{Details: "Code churn · {linesAddedFmt} added", State: "{linesNetFmt} net · {topLanguage}", Requires: Requires{"topLanguage"}},
						{Details: "Cost · {todayCostFmt} today", State: "{allCostFmt} all-time", Requires: Requires{"allCost"}},
						{Details: "Daily goal", State: "{goalLabel}", Requires: Requires{"goalLabel"}},
						{Details: "Monthly budget", State: "{budgetLabel}", Requires: Requires{"budgetLabel"}},
					},
				},
			},
		},
		StatusIcons: map[string]string{
			"working":      "working",
			"thinking":     "thinking",
			"compacting":   "thinking",
			"shipped":      "working",
			"idle":         "idle",
			"notification": "",
			"stale":        "",
		},
	}
}

// deepMerge merges plain maps recursively while replacing slices and primitives.
func deepMerge(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = cloneAny(v)
	}
	for k, v := range over {
		if v == nil {
			continue
		}
		bv, bok := out[k].(map[string]any)
		ov, ook := v.(map[string]any)
		if bok && ook {
			out[k] = deepMerge(bv, ov)
		} else {
			out[k] = cloneAny(v)
		}
	}
	return out
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = cloneAny(val)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, val := range t {
			s[i] = cloneAny(val)
		}
		return s
	default:
		return v
	}
}

func toMap(c Config) map[string]any {
	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func fromMap(m map[string]any) (Config, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return Default(), err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Default(), err
	}
	return cfg, nil
}

// LoadFrom reads config overrides at path and deep-merges them onto Default().
// Never panics; on unreadable/malformed JSON returns Default() and calls onErr.
func LoadFrom(path string, onErr func(string)) Config {
	def := Default()
	rawBytes, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && onErr != nil {
			onErr(fmt.Sprintf("config read failed at %s: %v — falling back to defaults", path, err))
		}
		return def
	}
	var over map[string]any
	if err := json.Unmarshal(rawBytes, &over); err != nil {
		if onErr != nil {
			onErr(fmt.Sprintf("config parse failed at %s: %v — falling back to defaults", path, err))
		}
		return def
	}
	merged := deepMerge(toMap(def), over)
	cfg, err := fromMap(merged)
	if err != nil {
		if onErr != nil {
			onErr(fmt.Sprintf("config decode failed at %s: %v — falling back to defaults", path, err))
		}
		return def
	}
	return cfg
}

func Load() Config {
	return LoadFrom(paths.ConfigPath(), nil)
}

var snowflakeRe = regexp.MustCompile(`^\d{17,21}$`)

func ClassifyClientID(id string) string {
	if id == "" || id == "1234567890123456789" {
		return "unset"
	}
	if !snowflakeRe.MatchString(id) {
		return "malformed"
	}
	return "ok"
}

func Validate(cfg Config) error {
	if ClassifyClientID(cfg.ClientID) != "ok" {
		return errors.New("Discord application ID must be a 17–21 digit number from the Discord developer portal")
	}
	if len(cfg.Presence.Buttons) > 2 {
		return errors.New("Discord shows at most 2 buttons")
	}
	for _, b := range cfg.Presence.Buttons {
		if strings.TrimSpace(b.Label) == "" || (!strings.HasPrefix(strings.ToLower(b.URL), "http://") && !strings.HasPrefix(strings.ToLower(b.URL), "https://")) {
			return errors.New("Each button needs a label and an http(s) link")
		}
	}
	if cfg.RotationIntervalMs < 0 || cfg.IdleThresholdSec < 0 || cfg.StaleSessionMin < 0 || cfg.ShippedFrameSec < 0 {
		return errors.New("Timing values must be 0 or greater")
	}
	return nil
}

// SaveDiff writes only the keys that changed between base and edited onto the
// raw config file on disk, so defaults are never frozen into config.json.
func SaveDiff(path string, base, edited Config) (Config, error) {
	if err := Validate(edited); err != nil {
		return Config{}, err
	}
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &raw); err != nil {
			return Config{}, fmt.Errorf("%s isn't valid JSON (%w)", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	next := ApplyEdits(raw, toMap(base), toMap(edited))
	out, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return Config{}, err
	}
	out = append(out, '\n')
	if err := paths.WriteFileAtomic(path, out, 0o644); err != nil {
		return Config{}, err
	}
	return LoadFrom(path, nil), nil
}

// ApplyEdits patches target with only the differences between base and edited.
func ApplyEdits(raw, base, edited map[string]any) map[string]any {
	out := cloneAny(raw).(map[string]any)
	patchMap(out, base, edited)
	return out
}

func patchMap(target, base, edited map[string]any) {
	keys := map[string]struct{}{}
	for k := range base {
		keys[k] = struct{}{}
	}
	for k := range edited {
		keys[k] = struct{}{}
	}
	for k := range keys {
		a, aOk := base[k]
		b, bOk := edited[k]
		if deepEqual(a, b) && aOk == bOk {
			continue
		}
		if !bOk || b == nil {
			delete(target, k)
			continue
		}
		am, aMap := a.(map[string]any)
		bm, bMap := b.(map[string]any)
		if aMap && bMap {
			_, had := target[k]
			tm, ok := target[k].(map[string]any)
			if !ok {
				tm = map[string]any{}
				target[k] = tm
			}
			patchMap(tm, am, bm)
			if !had && len(tm) == 0 {
				delete(target, k)
			}
			continue
		}
		target[k] = cloneAny(b)
	}
}

func deepEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
