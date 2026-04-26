package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type AlertEvent struct {
	Level         string
	MachineID     string
	IP            string
	CurrentCPU    float64
	ThresholdCPU  float64
	TriggerCount  int
	CooldownSec   int
	TriggeredAt   time.Time
	SourceService string
}

type Notifier interface {
	NotifyCPUAlert(ctx context.Context, event AlertEvent) error
	NotifyCPURecovery(ctx context.Context, event AlertEvent) error
}

type NoopNotifier struct{}

func (n *NoopNotifier) NotifyCPUAlert(_ context.Context, _ AlertEvent) error {
	return nil
}

func (n *NoopNotifier) NotifyCPURecovery(_ context.Context, _ AlertEvent) error {
	return nil
}

type WebhookNotifier struct {
	provider string
	url      string
	client   *http.Client
}

func NewWebhookNotifier(provider, url string, timeout time.Duration) *WebhookNotifier {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &WebhookNotifier{
		provider: strings.ToLower(strings.TrimSpace(provider)),
		url:      strings.TrimSpace(url),
		client:   &http.Client{Timeout: timeout},
	}
}

func (w *WebhookNotifier) NotifyCPUAlert(ctx context.Context, event AlertEvent) error {
	if w.url == "" {
		return fmt.Errorf("webhook url is empty")
	}

	content := fmt.Sprintf(
		"[%s] CPU告警\n节点: %s\nIP: %s\n当前CPU: %.1f%%\n阈值: %.1f%%\n连续次数: %d\n冷却: %ds\n时间: %s",
		event.Level,
		event.MachineID,
		event.IP,
		event.CurrentCPU,
		event.ThresholdCPU,
		event.TriggerCount,
		event.CooldownSec,
		event.TriggeredAt.Format(time.RFC3339),
	)

	body, err := w.buildPayload(content)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status=%d", resp.StatusCode)
	}

	return nil
}

func (w *WebhookNotifier) NotifyCPURecovery(ctx context.Context, event AlertEvent) error {
	if w.url == "" {
		return fmt.Errorf("webhook url is empty")
	}

	content := fmt.Sprintf(
		"[%s] CPU恢复\n节点: %s\nIP: %s\n当前CPU: %.1f%%\n阈值: %.1f%%\n此前连续次数: %d\n时间: %s",
		event.Level,
		event.MachineID,
		event.IP,
		event.CurrentCPU,
		event.ThresholdCPU,
		event.TriggerCount,
		event.TriggeredAt.Format(time.RFC3339),
	)

	body, err := w.buildPayload(content)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status=%d", resp.StatusCode)
	}

	return nil
}

func (w *WebhookNotifier) buildPayload(content string) ([]byte, error) {
	switch w.provider {
	case "dingtalk", "ding":
		payload := map[string]any{
			"msgtype": "text",
			"text": map[string]string{
				"content": content,
			},
		}
		return json.Marshal(payload)
	case "feishu", "lark", "":
		payload := map[string]any{
			"msg_type": "text",
			"content": map[string]string{
				"text": content,
			},
		}
		return json.Marshal(payload)
	default:
		return nil, fmt.Errorf("unsupported webhook provider: %s", w.provider)
	}
}

func initNotifier(cfg AppConfig) Notifier {
	notifierType := strings.ToLower(strings.TrimSpace(cfg.NotifierType))
	if notifierType == "" || notifierType == "noop" {
		return &NoopNotifier{}
	}

	if notifierType == "webhook" {
		if strings.TrimSpace(cfg.WebhookURL) == "" {
			return &NoopNotifier{}
		}
		timeout := time.Duration(cfg.NotifyTimeoutSeconds) * time.Second
		return NewWebhookNotifier(cfg.WebhookProvider, cfg.WebhookURL, timeout)
	}

	return &NoopNotifier{}
}
