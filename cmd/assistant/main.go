package main

import (
	"bytes"
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
	Diagnosis       *diagnosis    `json:"diagnosis,omitempty"`
	Notes           []string      `json:"notes"`
}

type appConfig struct {
	ListenAddr       string
	ServerBaseURL    string
	VictoriaBaseURL  string
	LokiBaseURL      string
	DefaultWindowMin int
	LLMEnabled       bool
	LLMBaseURL       string
	LLMAPIKey        string
	LLMModel         string
	LLMTimeoutSec    int
}

type diagnosis struct {
	Summary        string   `json:"summary"`
	Severity       string   `json:"severity"`
	PossibleCauses []string `json:"possible_causes"`
	Evidence       []string `json:"evidence"`
	NextSteps      []string `json:"next_steps"`
	Confidence     float64  `json:"confidence"`
}

type triageContext struct {
	MachineID       string        `json:"machine_id"`
	WindowMinutes   int           `json:"window_minutes"`
	AlertContext    []alertRecord `json:"alert_context"`
	MetricQueries   []string      `json:"metric_queries"`
	LogQueries      []string      `json:"log_queries"`
	LikelyCauses    []string      `json:"likely_causes"`
	RecommendedNext []string      `json:"recommended_next"`
}

func main() {
	cfg := appConfig{
		ListenAddr:       getEnv("ASSISTANT_LISTEN_ADDR", ":8090"),
		ServerBaseURL:    trimRightSlash(getEnv("ASSISTANT_SERVER_BASE_URL", "http://localhost:8080")),
		VictoriaBaseURL:  trimRightSlash(getEnv("ASSISTANT_VICTORIA_BASE_URL", "http://localhost:8428")),
		LokiBaseURL:      trimRightSlash(getEnv("ASSISTANT_LOKI_BASE_URL", "http://localhost:3100")),
		DefaultWindowMin: getEnvAsInt("ASSISTANT_DEFAULT_WINDOW_MINUTES", 15),
		LLMEnabled:       getEnvAsBool("LLM_ENABLED", false),
		LLMBaseURL:       trimRightSlash(getEnv("LLM_BASE_URL", "")),
		LLMAPIKey:        getEnv("LLM_API_KEY", ""),
		LLMModel:         getEnv("LLM_MODEL", "deepseek-chat"),
		LLMTimeoutSec:    getEnvAsInt("LLM_TIMEOUT_SECONDS", 15),
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
	metricQueries := buildMetricQueries(cfg, req)
	logQueries := buildLogQueries(cfg, req)
	likelyCauses := buildLikelyCauses(alerts)
	recommendedNext := buildRecommendedNext(alerts)
	resp := triageResponse{
		MachineID:       req.MachineID,
		WindowMinutes:   req.WindowMinutes,
		AlertContext:    alerts,
		MetricQueries:   metricQueries,
		LogQueries:      logQueries,
		LikelyCauses:    likelyCauses,
		RecommendedNext: recommendedNext,
		Notes:           notes,
	}

	if cfg.LLMEnabled {
		diag, err := diagnoseWithLLM(r.Context(), cfg, triageContext{
			MachineID:       req.MachineID,
			WindowMinutes:   req.WindowMinutes,
			AlertContext:    alerts,
			MetricQueries:   metricQueries,
			LogQueries:      logQueries,
			LikelyCauses:    likelyCauses,
			RecommendedNext: recommendedNext,
		})
		if err != nil {
			resp.Notes = append(resp.Notes, "llm diagnosis failed: "+err.Error())
		} else {
			resp.Diagnosis = diag
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func diagnoseWithLLM(ctx context.Context, cfg appConfig, input triageContext) (*diagnosis, error) {
	if cfg.LLMBaseURL == "" {
		return nil, fmt.Errorf("LLM_BASE_URL is empty")
	}
	if strings.TrimSpace(cfg.LLMModel) == "" {
		return nil, fmt.Errorf("LLM_MODEL is empty")
	}

	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	reqBody := map[string]any{
		"model": cfg.LLMModel,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": strings.Join([]string{
					"You are a read-only AIOps diagnostic assistant.",
					"Use only the provided alert, metric-query, log-query, and runbook-like context.",
					"Do not claim that you executed commands or changed infrastructure.",
					"Return only strict JSON with fields: summary, severity, possible_causes, evidence, next_steps, confidence.",
					"severity must be one of info, warning, critical.",
					"confidence must be a number from 0 to 1.",
				}, " "),
			},
			{
				"role":    "user",
				"content": "Diagnose this monitoring incident context:\n" + string(payload),
			},
		},
		"temperature": 0.2,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(cfg.LLMTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpoint := cfg.LLMBaseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.LLMAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	}

	client := &http.Client{Timeout: timeout + time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm endpoint returned status %d", resp.StatusCode)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("llm response has no choices")
	}

	content := cleanJSONContent(result.Choices[0].Message.Content)
	var diag diagnosis
	if err := json.Unmarshal([]byte(content), &diag); err != nil {
		return nil, fmt.Errorf("failed to parse llm diagnosis json: %w", err)
	}
	if diag.Summary == "" {
		return nil, fmt.Errorf("llm diagnosis summary is empty")
	}
	return &diag, nil
}

func cleanJSONContent(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
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

func getEnvAsBool(key string, defaultValue bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return defaultValue
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return defaultValue
	}
}

func trimRightSlash(value string) string {
	return strings.TrimRight(value, "/")
}
