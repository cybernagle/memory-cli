package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

type Notifier interface {
	Send(title, message string) error
	Name() string
}

type Config struct {
	DingDingWebhook string `yaml:"dingding_webhook"`
	DingDingSecret  string `yaml:"dingding_secret"`
	WeChatWebhook   string `yaml:"wechat_webhook"`
}

type MultiNotifier struct {
	notifiers []Notifier
}

func NewMultiNotifier(cfg Config) *MultiNotifier {
	mn := &MultiNotifier{}
	if cfg.DingDingWebhook != "" {
		mn.notifiers = append(mn.notifiers, &DingDingNotifier{Webhook: cfg.DingDingWebhook, Secret: cfg.DingDingSecret})
	}
	if cfg.WeChatWebhook != "" {
		mn.notifiers = append(mn.notifiers, &WeChatNotifier{Webhook: cfg.WeChatWebhook})
	}
	return mn
}

func (mn *MultiNotifier) Send(title, message string) error {
	var errs []error
	for _, n := range mn.notifiers {
		if err := n.Send(title, message); err != nil {
			log.Printf("[%s] send failed: %v", n.Name(), err)
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d notifier(s) failed", len(errs))
	}
	return nil
}

func (mn *MultiNotifier) HasNotifiers() bool {
	return len(mn.notifiers) > 0
}

func FormatMessage(title, body string) string {
	return fmt.Sprintf("## %s\n> %s\n> %s", title, body, time.Now().Format("2006-01-02 15:04:05"))
}

func postJSON(url string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
