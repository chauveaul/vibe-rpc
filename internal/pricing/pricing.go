package pricing

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type Rates struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

var Table = map[string]Rates{
	// Gemini 4
	"gemini-4-neon":       {Input: 2.50, Output: 15.00, CacheRead: 0.25},
	"gemini-4-pro":        {Input: 2.50, Output: 15.00, CacheRead: 0.25},
	"gemini-4-flash":      {Input: 0.50, Output: 3.00, CacheRead: 0.05},
	"gemini-4-flash-lite": {Input: 0.15, Output: 0.60, CacheRead: 0.02},
	// Gemini 3
	"gemini-3-pro":        {Input: 2.00, Output: 12.00, CacheRead: 0.20},
	"gemini-3-flash":      {Input: 0.50, Output: 3.00, CacheRead: 0.05},
	"gemini-3-flash-lite": {Input: 0.10, Output: 0.40, CacheRead: 0.01},
	// Gemini 2.5
	"gemini-2-5-pro":        {Input: 1.25, Output: 10.00, CacheRead: 0.31},
	"gemini-2-5-flash":      {Input: 0.30, Output: 2.50, CacheRead: 0.075},
	"gemini-2-5-flash-lite": {Input: 0.10, Output: 0.40, CacheRead: 0.025},
	// Gemini 2.0
	"gemini-2-0-flash": {Input: 0.10, Output: 0.40, CacheRead: 0.025},
	"gemini-2-0-pro":   {Input: 1.25, Output: 5.00, CacheRead: 0.31},
	// Generic Gemini
	"gemini-neon":       {Input: 2.50, Output: 15.00, CacheRead: 0.25},
	"gemini-pro":        {Input: 2.00, Output: 12.00, CacheRead: 0.20},
	"gemini-flash":      {Input: 0.50, Output: 3.00, CacheRead: 0.05},
	"gemini-flash-lite": {Input: 0.10, Output: 0.40, CacheRead: 0.01},
	// Claude families
	"fable-5":    {Input: 10.00, Output: 50.00, CacheRead: 1.00, CacheWrite: 12.50},
	"fable-5-1":  {Input: 10.00, Output: 50.00, CacheRead: 0.25, CacheWrite: 12.50},
	"fable-5-2":  {Input: 8.00, Output: 40.00, CacheRead: 0.20, CacheWrite: 10.00},
	"opus-5":     {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"opus-5-5":   {Input: 4.00, Output: 20.00, CacheRead: 0.20, CacheWrite: 5.00},
	"sonnet-5":   {Input: 2.00, Output: 10.00, CacheRead: 0.20, CacheWrite: 2.50},
	"sonnet-5-5": {Input: 2.00, Output: 10.00, CacheRead: 0.10, CacheWrite: 2.50},
	"haiku-5":    {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125},
	"opus-4":     {Input: 15.00, Output: 75.00, CacheRead: 1.50, CacheWrite: 18.75},
	"opus-4-5":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"opus-4-6":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"opus-4-7":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"sonnet-4":   {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	"sonnet-4-5": {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	"sonnet-4-6": {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	"haiku-4-5":  {Input: 1.00, Output: 5.00, CacheRead: 0.10, CacheWrite: 1.25},
	"opus":       {Input: 4.00, Output: 20.00, CacheRead: 0.20, CacheWrite: 5.00},
	"sonnet":     {Input: 2.00, Output: 10.00, CacheRead: 0.10, CacheWrite: 2.50},
	"haiku":      {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125},
	"fable":      {Input: 8.00, Output: 40.00, CacheRead: 0.20, CacheWrite: 10.00},
}

var (
	bracketRe = regexp.MustCompile(`\[[^\]]*\]`)
	verRe     = regexp.MustCompile(`^\d{1,2}$`)
	dateRe    = regexp.MustCompile(`^\d{4,}$`)
)

var claudeTiers = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true, "mythos": true}
var effortSuffix = map[string]bool{"high": true, "medium": true, "low": true, "minimal": true, "thinking": true, "preview": true, "exp": true, "latest": true, "fast": true}

func parseClaude(raw string) (key, tier string, ok bool) {
	s := bracketRe.ReplaceAllString(strings.ToLower(raw), "")
	parts := strings.Split(s, "-")
	tierIdx := -1
	for i, p := range parts {
		if claudeTiers[p] {
			tierIdx = i
			break
		}
	}
	if tierIdx == -1 {
		return "", "", false
	}
	tier = parts[tierIdx]
	var major, minor int
	hasMajor := false
	if tierIdx+1 < len(parts) && verRe.MatchString(parts[tierIdx+1]) {
		major, _ = strconv.Atoi(parts[tierIdx+1])
		hasMajor = true
		if tierIdx+2 < len(parts) && verRe.MatchString(parts[tierIdx+2]) {
			minor, _ = strconv.Atoi(parts[tierIdx+2])
		}
	}
	if !hasMajor {
		return tier, tier, true
	}
	if minor > 0 {
		return fmt.Sprintf("%s-%d-%d", tier, major, minor), tier, true
	}
	return fmt.Sprintf("%s-%d", tier, major), tier, true
}

func parseGemini(raw string) (key, tier string, ok bool) {
	s := strings.ReplaceAll(bracketRe.ReplaceAllString(strings.ToLower(raw), ""), ".", "-")
	rawParts := strings.Split(s, "-")
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if p != "" {
			parts = append(parts, p)
		}
	}
	gIdx := -1
	for i, p := range parts {
		if p == "gemini" {
			gIdx = i
			break
		}
	}
	if gIdx == -1 {
		switch {
		case strings.Contains(s, "flash_lite") || strings.Contains(s, "flash-lite"):
			return "gemini-flash-lite", "flash-lite", true
		case strings.Contains(s, "neon"):
			return "gemini-neon", "neon", true
		case strings.Contains(s, "flash"):
			return "gemini-flash", "flash", true
		case strings.Contains(s, "pro"):
			return "gemini-pro", "pro", true
		default:
			return "", "", false
		}
	}
	rest := parts[gIdx+1:]
	i := 0
	var major, minor int
	hasMajor := false
	if i < len(rest) && verRe.MatchString(rest[i]) {
		major, _ = strconv.Atoi(rest[i])
		hasMajor = true
		i++
		if i < len(rest) && verRe.MatchString(rest[i]) {
			minor, _ = strconv.Atoi(rest[i])
			i++
		}
	}
	var tierToks []string
	for i < len(rest) {
		tok := rest[i]
		i++
		if effortSuffix[tok] || dateRe.MatchString(tok) {
			break
		}
		if tok == "lite" && len(tierToks) > 0 && tierToks[len(tierToks)-1] == "flash" {
			tierToks = append(tierToks, tok)
		} else if len(tierToks) == 0 {
			tierToks = append(tierToks, tok)
		}
	}
	tier = strings.Join(tierToks, "-")
	if tier == "" {
		tier = "pro"
	}
	if !hasMajor {
		return "gemini-" + tier, tier, true
	}
	if minor > 0 {
		return fmt.Sprintf("gemini-%d-%d-%s", major, minor, tier), tier, true
	}
	return fmt.Sprintf("gemini-%d-%s", major, tier), tier, true
}

func PricingKeyFor(modelID string) string {
	if modelID == "" {
		return "gemini-pro"
	}
	if key, tier, ok := parseClaude(modelID); ok {
		if _, exists := Table[key]; exists {
			return key
		}
		if _, exists := Table[tier]; exists {
			return tier
		}
		return "sonnet"
	}
	if key, tier, ok := parseGemini(modelID); ok {
		if _, exists := Table[key]; exists {
			return key
		}
		if _, exists := Table["gemini-"+tier]; exists {
			return "gemini-" + tier
		}
		return "gemini-pro"
	}
	return "gemini-pro"
}

func ModelKeyFor(modelID string) string {
	if key, _, ok := parseClaude(modelID); ok {
		return key
	}
	if key, _, ok := parseGemini(modelID); ok {
		return key
	}
	return PricingKeyFor(modelID)
}

func RatesFor(modelID string) Rates {
	if r, ok := Table[PricingKeyFor(modelID)]; ok {
		return r
	}
	return Table["gemini-pro"]
}

func CostFor(modelID string, u Usage) float64 {
	r := RatesFor(modelID)
	const m = 1_000_000.0
	return (float64(u.InputTokens)*r.Input +
		float64(u.OutputTokens)*r.Output +
		float64(u.CacheReadTokens)*r.CacheRead +
		float64(u.CacheWriteTokens)*r.CacheWrite) / m
}

func FmtCost(usd float64) string {
	if usd <= 0 {
		return "$0"
	}
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	if usd < 100 {
		return fmt.Sprintf("$%.2f", usd)
	}
	if usd < 1000 {
		return fmt.Sprintf("$%.0f", math.Round(usd))
	}
	if usd < 10_000 {
		return fmt.Sprintf("$%.2fk", usd/1000)
	}
	return fmt.Sprintf("$%.1fk", usd/1000)
}
