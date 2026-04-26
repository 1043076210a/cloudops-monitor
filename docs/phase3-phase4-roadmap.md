# Phase 3 / Phase 4 Roadmap

This roadmap continues the current Go monitoring platform incrementally. The
existing HTTP ingest path, Kafka mode, MySQL metadata store, VictoriaMetrics
metrics store, Redis alert state, webhook notification path, health checks,
structured logs, Grafana dashboard, and k6 script remain the baseline.

## Principles

- Keep the current direct and Kafka ingest modes working.
- Prefer one runnable vertical slice at a time over a broad rewrite.
- Make every new component demonstrable locally before hardening it for
  production Kubernetes.
- Keep responsibilities split: MySQL for metadata and alert events,
  VictoriaMetrics for metrics, Redis for alert state and cooldown, Loki for
  logs, Jaeger for traces.
- Add Kubernetes manifests after the container behavior is stable in Compose.

## Current State

Implemented:

- Agent auto-detects host information, registers itself, and reports CPU,
  memory, and disk metrics.
- Server supports `INGEST_MODE=direct` and `INGEST_MODE=kafka`.
- Redpanda/Kafka is available in Docker Compose.
- Worker consumes metric events and forwards them to
  `/api/v1/metrics/process`.
- MySQL stores machine metadata and alert events.
- VictoriaMetrics stores time-series metrics.
- Redis stores alert breach counters and cooldown state.
- Webhook alert and recovery notifications are available.
- Health endpoints, structured JSON logs, Grafana provisioning, and k6 load
  testing are present.

## Phase 3: Observability and Kubernetes Minimum Demo

Primary goal: make the platform observable as a cloud-native system and deploy
it to Kubernetes with the smallest reliable manifests.

### 3.1 Local log aggregation with Loki

Minimal design:

- Add Loki and Promtail to `deployments/docker-compose.yml`.
- Configure Promtail to scrape Docker container logs.
- Add a Loki datasource to Grafana provisioning.
- Keep the application logging format as JSON; do not add a new logging
  framework unless structured fields become inconsistent.

Files:

- `deployments/docker-compose.yml`: add `loki` and `promtail` services.
- `deployments/promtail/promtail.yml`: Docker log scrape config.
- `deployments/grafana/provisioning/datasources/datasource.yml`: add Loki.
- `docs/runbook.md`: add log query examples.

Demo validation:

- Start the stack with `docker compose up -d`.
- Generate traffic through the agent or k6.
- In Grafana Explore, query `{container_name=~"cloudops-.*"}`.
- Confirm server, worker, Redpanda, and dependency logs are searchable.

Exit criteria:

- Operators can inspect ingest, alert, and worker logs without reading local
  terminal output.

### 3.2 OpenTelemetry traces and Jaeger

Minimal design:

- Add OpenTelemetry SDK and Gin middleware to the server.
- Add trace spans around:
  - `/api/v1/metrics`
  - Kafka publish
  - `/api/v1/metrics/process`
  - VictoriaMetrics write
  - alert evaluation
  - notification send
- Add Jaeger and OTel Collector to Compose.
- Export traces through the collector, not directly to Jaeger, so the same app
  config works later in Kubernetes.

Files:

- `go.mod` / `go.sum`: add OpenTelemetry dependencies.
- `config.go`: add `OTEL_ENABLED`, `OTEL_SERVICE_NAME`,
  `OTEL_EXPORTER_OTLP_ENDPOINT`.
- `observability.go` or `internal/observability/tracing.go`: initialize and
  shut down tracing.
- `main.go`: install Gin tracing middleware and add spans in ingest paths.
- `cmd/worker/main.go`: add worker trace propagation later; first slice can
  keep worker logs only.
- `deployments/otel-collector-config.yml`: receive OTLP, export to Jaeger.
- `deployments/docker-compose.yml`: add `otel-collector` and `jaeger`.

Demo validation:

- Send one direct metric request.
- Send one Kafka metric request and process it through the worker.
- Open Jaeger and confirm traces for server endpoints.
- Confirm trace IDs are present in logs if practical.

Exit criteria:

- A single metric ingest request can be followed from HTTP handler to TSDB
  write and alert evaluation.

### 3.3 Service-level metrics for the platform itself

Minimal design:

- Expose `/metrics` from server and worker.
- Track ingest request count, publish failures, worker forward failures,
  VictoriaMetrics write failures, alert notifications, and request latency.
- Keep host metrics in VictoriaMetrics as-is; these new metrics describe the
  platform services.

Files:

- `go.mod` / `go.sum`: add Prometheus Go client dependencies.
- `main.go`: expose `/metrics` and instrument server paths.
- `cmd/worker/main.go`: expose a worker metrics endpoint or defer this until
  worker runs as a proper HTTP process.
- `deployments/grafana/dashboards/cloudops-overview.json`: add platform panels.

Demo validation:

- `curl http://localhost:8080/metrics`.
- Run k6 and confirm counters increase.
- Show ingest success and failure trends in Grafana.

Exit criteria:

- The platform can monitor its own ingest health, not just host health.

### 3.4 Kubernetes YAML MVP

Minimal design:

- Add plain YAML first, not Helm.
- Deploy stateless components as Deployments:
  - server
  - worker
  - agent as DaemonSet
- Use Services for server, VictoriaMetrics, MySQL, Redis, Redpanda, Grafana,
  Loki, OTel Collector, and Jaeger as needed.
- Use ConfigMaps for non-secret config and Secrets for DSNs/passwords.
- For demo mode, external dependencies may still run from Docker Compose or
  lightweight in-cluster manifests. Pick one path per demo to avoid confusion.

Files:

- `deployments/k8s/namespace.yaml`
- `deployments/k8s/server-deployment.yaml`
- `deployments/k8s/server-service.yaml`
- `deployments/k8s/worker-deployment.yaml`
- `deployments/k8s/agent-daemonset.yaml`
- `deployments/k8s/configmap.yaml`
- `deployments/k8s/secret.example.yaml`
- `deployments/k8s/victoriametrics.yaml`
- `deployments/k8s/redis.yaml`
- `deployments/k8s/mysql.yaml`
- `deployments/k8s/redpanda.yaml`
- `deployments/k8s/grafana.yaml`
- `deployments/k8s/loki.yaml`
- `deployments/k8s/otel-collector.yaml`
- `deployments/k8s/jaeger.yaml`

Demo validation:

- `kubectl apply -f deployments/k8s`.
- `kubectl get pods -n cloudops`.
- Port-forward Grafana, server, Jaeger, and VictoriaMetrics.
- Confirm the agent DaemonSet registers nodes and sends metrics.
- Confirm logs in Loki and traces in Jaeger.

Exit criteria:

- A reviewer can run the whole platform on a local cluster such as kind,
  minikube, or Docker Desktop Kubernetes.

## Phase 4: Kubernetes Governance and Intelligent Operations

Primary goal: move from "runs on Kubernetes" to "is operated through Kubernetes
patterns", then add an LLM assistant on top of stable data sources.

### 4.1 Prometheus Operator integration

Minimal design:

- Introduce Prometheus Operator or kube-prometheus-stack for platform service
  scraping and alert rule governance.
- Keep VictoriaMetrics for host metric storage unless replacing it becomes an
  explicit decision.
- Use `ServiceMonitor` objects to scrape server and worker `/metrics`.
- Use `PrometheusRule` objects for platform health alerts:
  - ingest error rate
  - worker forward failure rate
  - Kafka consumer lag, if exported
  - VictoriaMetrics write failures
  - notification failures

Files:

- `deployments/k8s/monitoring/service-monitor-server.yaml`
- `deployments/k8s/monitoring/service-monitor-worker.yaml`
- `deployments/k8s/monitoring/prometheus-rules.yaml`
- `docs/runbook.md`: add Prometheus Operator install and validation notes.

Demo validation:

- Install kube-prometheus-stack.
- Apply ServiceMonitor and PrometheusRule manifests.
- Confirm targets are discovered.
- Trigger a controlled ingest failure and confirm alert evaluation.

Exit criteria:

- Platform service monitoring is managed by Kubernetes CRDs instead of manual
  scrape configuration.

### 4.2 Mature Kubernetes collection governance

Minimal design:

- Standardize labels and annotations across all workloads.
- Move agent config into a ConfigMap.
- Use RBAC only where needed.
- Add resource requests/limits, probes, PodDisruptionBudgets for server and
  worker, and persistent volume claims for stateful demo dependencies.
- Define retention and storage knobs for VictoriaMetrics and Loki.

Files:

- Existing `deployments/k8s/*.yaml`: add labels, probes, resources, and RBAC.
- `deployments/k8s/agent-rbac.yaml`: only if the agent reads Kubernetes node or
  pod metadata.
- `docs/k8s-operations.md`: operational notes and troubleshooting.

Demo validation:

- Restart server and worker pods; confirm no data path break.
- Kill one worker pod; confirm Kafka mode recovers.
- Confirm readiness gates prevent traffic before dependencies are ready.

Exit criteria:

- The demo behaves predictably during pod restarts and dependency failures.

### 4.3 Custom Operator MVP

Minimal design:

- Build an operator only after the YAML manifests are stable.
- Start with a small CRD, for example `CloudOpsMonitor`, that installs and
  reconciles:
  - server Deployment and Service
  - worker Deployment
  - agent DaemonSet
  - ConfigMap references
- Do not put MySQL, Redis, Kafka, Loki, or VictoriaMetrics lifecycle management
  into the first operator slice. Reference existing services first.

Suggested CRD shape:

```yaml
apiVersion: cloudops.example.com/v1alpha1
kind: CloudOpsMonitor
metadata:
  name: cloudops
spec:
  ingestMode: kafka
  server:
    replicas: 2
    image: cloudops/server:dev
  worker:
    replicas: 2
    image: cloudops/worker:dev
  agent:
    image: cloudops/agent:dev
  dependencies:
    mysqlDSNSecretRef: cloudops-mysql
    redisAddr: cloudops-redis:6379
    kafkaBrokers: cloudops-redpanda:9092
    victoriaMetricsURL: http://cloudops-vm:8428/api/v1/import/prometheus
```

Files:

- `operator/`: new Go operator module, preferably scaffolded with
  kubebuilder.
- `operator/api/v1alpha1/cloudopsmonitor_types.go`
- `operator/controllers/cloudopsmonitor_controller.go`
- `operator/config/samples/cloudops_v1alpha1_cloudopsmonitor.yaml`

Demo validation:

- Apply CRD and operator.
- Apply one `CloudOpsMonitor` custom resource.
- Confirm server, worker, and agent are reconciled.
- Change `spec.worker.replicas`; confirm the Deployment updates.

Exit criteria:

- The platform's core workloads can be created and adjusted through one custom
  resource.

### 4.4 LLM Ops Assistant MVP

Minimal design:

- Add an assistant as a separate service, not inside the ingest hot path.
- First version should be read-only and deterministic:
  - fetch recent alerts from MySQL through existing API or a new internal API
  - query VictoriaMetrics for recent host trends
  - query Loki for related logs
  - optionally link to Jaeger traces by trace ID
  - return a triage summary and runbook suggestions
- Keep all commands manual in the first version. Do not let the assistant
  mutate Kubernetes resources until audit, auth, and approval flows exist.

Files:

- `cmd/assistant/main.go`: read-only assistant API.
- `internal/assistant/`: clients for alerts, VictoriaMetrics, Loki, and runbook
  retrieval.
- `docs/runbook.md`: structure sections so they can be retrieved by alert type.
- `deployments/k8s/assistant-deployment.yaml`
- `deployments/k8s/assistant-service.yaml`

Example API:

```http
POST /api/v1/assistant/triage
{
  "machine_id": "machine-001",
  "alert_id": 123,
  "window_minutes": 15
}
```

Demo validation:

- Trigger a CPU alert.
- Call the assistant triage endpoint.
- Confirm the response includes:
  - alert context
  - metric trend summary
  - relevant log snippets or log query links
  - likely causes
  - next manual checks from the runbook

Exit criteria:

- Operators get a useful first-pass incident summary without leaving the
  monitoring platform.

## Recommended Execution Order

1. Add Loki and Promtail to the local Compose stack.
2. Add OpenTelemetry tracing in the server and wire OTel Collector plus Jaeger.
3. Expose platform `/metrics` and add Grafana panels for ingest health.
4. Add Kubernetes YAML MVP for server, worker, agent, and dependencies.
5. Add Prometheus Operator `ServiceMonitor` and `PrometheusRule` resources.
6. Harden K8s manifests with probes, resources, RBAC, labels, and storage.
7. Build the custom Operator MVP around stable YAML.
8. Build the read-only LLM Ops Assistant.

## Implemented Phase 4 Slices

- Prometheus Operator resources:
  - `deployments/k8s/monitoring/servicemonitor-server.yaml`
  - `deployments/k8s/monitoring/servicemonitor-worker.yaml`
  - `deployments/k8s/monitoring/prometheus-rules.yaml`
- Custom Operator API shape:
  - `deployments/k8s/operator/cloudopsmonitor-crd.yaml`
  - `deployments/k8s/operator/cloudopsmonitor-sample.yaml`
  - `cmd/operator/main.go`
  - `Dockerfile.operator`
  - `deployments/k8s/operator/operator-deployment.yaml`
- Read-only assistant MVP:
  - `cmd/assistant/main.go`
  - `Dockerfile.assistant`
  - `deployments/k8s/assistant-deployment.yaml`

## Near-Term Next Slice

The best immediate next slice is Loki plus Promtail in Docker Compose because
it does not affect the ingest path and demonstrates value quickly. After that,
add OTel tracing to the server so logs, metrics, and traces can be shown
together before moving to Kubernetes YAML.
