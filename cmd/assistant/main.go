package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type triageRequest struct {
	MachineID     string `json:"machine_id"`
	AlertID       int    `json:"alert_id"`
	WindowMinutes int    `json:"window_minutes"`
}

type alertRecord struct {
	ID             uint      `json:"id"`
	MachineID      string    `json:"machine_id"`
	IP             string    `json:"ip"`
	Level          string    `json:"level"`
	Metric         string    `json:"metric"`
	ThresholdValue float64   `json:"threshold_value"`
	CurrentValue   float64   `json:"current_value"`
	TriggerCount   int       `json:"trigger_count"`
	CooldownSec    int       `json:"cooldown_sec"`
	NotifyStatus   string    `json:"notify_status"`
	NotifyError    string    `json:"notify_error"`
	TriggeredAt    time.Time `json:"triggered_at"`
	CreatedAt      time.Time `json:"created_at"`
}

type recentAlertsResponse struct {
	Count  int           `json:"count"`
	Events []alertRecord `json:"events"`
}

type triageResponse struct {
	MachineID       string        `json:"machine_id"`
	WindowMinutes   int           `json:"window_minutes"`
	AlertContext    []alertRecord `json:"alert_context"`
	MetricQueries   []string      `json:"metric_queries"`
	LogQueries      []string      `json:"log_queries"`
	LikelyCauses    []string      `json:"likely_causes"`
	RecommendedNext []string      `json:"recommended_next"`
	Notes           []string      `json:"notes"`
}

type appConfig struct {
	ListenAddr       string
	ServerBaseURL    string
	VictoriaBaseURL  string
	LokiBaseURL      string
	DefaultWindowMin int
}

func main() {
	cfg := appConfig{
		ListenAddr:       getEnv("ASSISTANT_LISTEN_ADDR", ":8090"),
		ServerBaseURL:    trimRightSlash(getEnv("ASSISTANT_SERVER_BASE_URL", "http://localhost:8080")),
		VictoriaBaseURL:  trimRightSlash(getEnv("ASSISTANT_VICTORIA_BASE_URL", "http://localhost:8428")),
		LokiBaseURL:      trimRightSlash(getEnv("ASSISTANT_LOKI_BASE_URL", "http://localhost:3100")),
		DefaultWindowMin: getEnvAsInt("ASSISTANT_DEFAULT_WINDOW_MINUTES", 15),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/assistant/triage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		handleTriage(w, r, cfg)
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("assistant listening on %s", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func handleTriage(w http.ResponseWriter, r *http.Request, cfg appConfig) {
	var req triageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.MachineID = strings.TrimSpace(req.MachineID)
	if req.MachineID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "machine_id cannot be empty"})
		return
	}
	if req.WindowMinutes <= 0 {
		req.WindowMinutes = cfg.DefaultWindowMin
	}

	alerts, notes := fetchRecentAlerts(r.Context(), cfg, req)
	resp := triageResponse{
		MachineID:       req.MachineID,
		WindowMinutes:   req.WindowMinutes,
		AlertContext:    alerts,
		MetricQueries:   buildMetricQueries(cfg, req),
		LogQueries:      buildLogQueries(cfg, req),
		LikelyCauses:    buildLikelyCauses(alerts),
		RecommendedNext: buildRecommendedNext(alerts),
		Notes:           notes,
	}
	writeJSON(w, http.StatusOK, resp)
}

func fetchRecentAlerts(ctx context.Context, cfg appConfig, req triageRequest) ([]alertRecord, []string) {
	params := url.Values{}
	params.Set("machine_id", req.MachineID)
	params.Set("limit", "5")
	requestURL := cfg.ServerBaseURL + "/api/v1/alerts/recent?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, []string{"failed to build recent-alert request: " + err.Error()}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, []string{"failed to fetch recent alerts: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, []string{fmt.Sprintf("recent alerts endpoint returned status %d", resp.StatusCode)}
	}

	var payload recentAlertsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, []string{"failed to decode recent alerts: " + err.Error()}
	}

	if req.AlertID > 0 {
		for _, event := range payload.Events {
			if int(event.ID) == req.AlertID {
				return []alertRecord{event}, nil
			}
		}
		return payload.Events, []string{fmt.Sprintf("alert_id %d was not found in recent alert window", req.AlertID)}
	}
	return payload.Events, nil
}

func buildMetricQueries(cfg appConfig, req triageRequest) []string {
	end := time.Now()
	start := end.Add(-time.Duration(req.WindowMinutes) * time.Minute)
	step := "30s"
	return []string{
		vmRangeURL(cfg, `host_cpu_usage{machine_id="`+req.MachineID+`"}`, start, end, step),
		vmRangeURL(cfg, `host_mem_usage{machine_id="`+req.MachineID+`"}`, start, end, step),
		vmRangeURL(cfg, `host_disk_usage{machine_id="`+req.MachineID+`"}`, start, end, step),
	}
}

func buildLogQueries(cfg appConfig, req triageRequest) []string {
	query := `{namespace="cloudops"} |= "` + req.MachineID + `"`
	values := url.Values{}
	values.Set("query", query)
	values.Set("limit", "100")
	return []string{cfg.LokiBaseURL + "/loki/api/v1/query_range?" + values.Encode()}
}

func vmRangeURL(cfg appConfig, query string, start, end time.Time, step string) string {
	values := url.Values{}
	values.Set("query", query)
	values.Set("start", strconv.FormatInt(start.Unix(), 10))
	values.Set("end", strconv.FormatInt(end.Unix(), 10))
	values.Set("step", step)
	return cfg.VictoriaBaseURL + "/api/v1/query_range?" + values.Encode()
}

func buildLikelyCauses(alerts []alertRecord) []string {
	for _, alert := range alerts {
		switch alert.Metric {
		case "cpu_usage":
			return []string{
				"CPU usage exceeded the configured sustained threshold.",
				"Likely causes include a bursty process, overloaded service, tight loop, or insufficient CPU limit.",
			}
		}
	}
	return []string{
		"No recent alert-specific cause was found.",
		"Review metric trend links and related logs for the selected machine.",
	}
}

func buildRecommendedNext(alerts []alertRecord) []string {
	next := []string{
		"Open the metric query links and compare CPU, memory, and disk trends over the incident window.",
		"Check Loki logs for the same machine_id around the alert timestamp.",
		"Check Jaeger traces for slow ingest or downstream write errors if the incident involves pipeline health.",
	}
	for _, alert := range alerts {
		if alert.NotifyStatus == "failed" {
			next = append(next, "Notification failed for at least one alert; verify webhook configuration and provider response.")
			break
		}
	}
	return next
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func getEnv(key, defaultValue string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func trimRightSlash(value string) string {
	return strings.TrimRight(value, "/")
}
