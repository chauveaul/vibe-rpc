package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/chauveaul/vibe-rpc/internal/config"
)

func SanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == ' ' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func Desktop(title, body string) {
	go func() {
		switch runtime.GOOS {
		case "darwin":
			tb, _ := json.Marshal(title)
			bb, _ := json.Marshal(body)
			script := fmt.Sprintf("display notification %s with title %s", string(bb), string(tb))
			_ = exec.Command("osascript", "-e", script).Run()
		case "windows":
			tb, _ := json.Marshal(title)
			bb, _ := json.Marshal(body)
			script := fmt.Sprintf("Add-Type -AssemblyName System.Windows.Forms;$n=New-Object System.Windows.Forms.NotifyIcon;$n.Icon=[System.Drawing.SystemIcons]::Information;$n.Visible=$true;$n.ShowBalloonTip(5000,%s,%s,'Info');Start-Sleep -Seconds 6;$n.Dispose()", string(tb), string(bb))
			_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
		default:
			_ = exec.Command("notify-send", "-a", "Antigravity", title, body).Run()
		}
	}()
}

func PostWebhook(url string, payload any) {
	if url == "" {
		return
	}
	go func() { _, _ = PostWebhookSync(url, payload) }()
}

// PostWebhookSync POSTs payload as JSON and returns the HTTP status code.
// Non-2xx responses are reported as errors.
func PostWebhookSync(url string, payload any) (int, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func ShouldNotify(cfg config.NotifyConfig, prevStatus, newStatus string) bool {
	if !cfg.Enabled || prevStatus == newStatus {
		return false
	}
	onNotif := cfg.OnNotification == nil || *cfg.OnNotification
	return onNotif && newStatus == "notification"
}

func ShouldWebhook(cfg config.WebhookConfig, prevStatus, newStatus string) bool {
	if cfg.URL == "" || prevStatus == newStatus {
		return false
	}
	for _, s := range cfg.On {
		if s == newStatus {
			return true
		}
	}
	return false
}
