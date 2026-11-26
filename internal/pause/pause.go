package pause

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/meze/agy-rpc/internal/paths"
)

const DefaultDuration = time.Hour

var hmRe = regexp.MustCompile(`^(?:(\d+)h)?(?:(\d+)m)?$`)

func ParseDuration(raw string) (time.Duration, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return DefaultDuration, true
	}
	if mins, err := strconv.Atoi(s); err == nil {
		if mins > 0 {
			return time.Duration(mins) * time.Minute, true
		}
		return 0, false
	}
	m := hmRe.FindStringSubmatch(s)
	if m == nil || (m[1] == "" && m[2] == "") {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mins, _ := strconv.Atoi(m[2])
	total := h*60 + mins
	if total <= 0 {
		return 0, false
	}
	return time.Duration(total) * time.Minute, true
}

func Set(d time.Duration, file string) (time.Time, error) {
	if file == "" {
		file = paths.PausePath()
	}
	until := time.Now().Add(d)
	b, _ := json.Marshal(struct {
		Until int64 `json:"until"`
	}{Until: until.UnixMilli()})
	return until, paths.WriteFileAtomic(file, b, 0o644)
}

func Clear(file string) bool {
	if file == "" {
		file = paths.PausePath()
	}
	if err := os.Remove(file); err == nil {
		return true
	}
	return false
}

func Until(file string) time.Time {
	if file == "" {
		file = paths.PausePath()
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return time.Time{}
	}
	var payload struct {
		Until int64 `json:"until"`
	}
	if err := json.Unmarshal(b, &payload); err != nil || payload.Until <= 0 {
		return time.Time{}
	}
	t := time.UnixMilli(payload.Until)
	if !t.After(time.Now()) {
		return time.Time{}
	}
	return t
}
