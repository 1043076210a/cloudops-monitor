# Local Runbook

## 1) Dependency startup
- `cd deployments`
- `docker compose up -d`

## 2) Server startup
- `cd ..`
- `go run .`

## 3) Agent startup
- `cd agent`
- `go run .`

## 4) Health checks
- `GET http://localhost:8080/ping`
- `GET http://localhost:8080/healthz/deps`

## 5) API manual test
Use `test.http` in VS Code REST Client.

## 6) Troubleshooting
- 8080 occupied:
  - `netstat -ano | findstr :8080`
  - `taskkill /F /PID <PID>`
- Dependencies not ready:
  - `docker ps`
  - check MySQL/Redis/VictoriaMetrics containers.

## 7) Data observation
- VictoriaMetrics UI: `http://localhost:8428/vmui`
- Grafana: `http://localhost:3000`
- MySQL tables: `machines`, `alert_event_records`
- Server platform metrics: `http://localhost:8080/metrics`
- Worker platform metrics: `http://localhost:9091/metrics`

Useful platform metric names:
- `cloudops_server_ingest_requests_total`
- `cloudops_server_kafka_publish_total`
- `cloudops_server_vm_writes_total`
- `cloudops_server_vm_write_duration_seconds`
- `cloudops_server_metrics_process_duration_seconds`
- `cloudops_server_alert_notifications_total`
- `cloudops_worker_kafka_messages_total`
- `cloudops_worker_forward_total`
- `cloudops_worker_forward_duration_seconds`
- `cloudops_worker_processing_duration_seconds`

## 8) Log observation with Loki
- Start the local stack from `deployments`: `docker compose up -d`.
- Open Grafana Explore: `http://localhost:3000/explore`.
- Select the `Loki` datasource.
- Useful LogQL queries:
  - `{container_name=~"cloudops-.*"}`
  - `{container_name="cloudops-loki"}`
  - `{compose_service="grafana"}`
- Generate traffic with the agent or k6, then query recent logs for ingest and
  alert events.

If no logs appear:
- Check Promtail: `docker logs cloudops-promtail`.
- Check Loki: `docker logs cloudops-loki`.
- Confirm Docker container log mounts are available to the Promtail container.

## 9) Trace observation with OTel and Jaeger
- Start the local stack from `deployments`: `docker compose up -d`.
- Open Jaeger: `http://localhost:16686`.
- Confirm the Jaeger UI loads.
- The OTel Collector endpoint is available on `localhost:4317` and
  `localhost:4318`.
- Start the server with tracing enabled:
  - PowerShell:
    - `$env:OTEL_ENABLED="true"`
    - `$env:OTEL_EXPORTER_OTLP_ENDPOINT="localhost:4317"`
    - `$env:OTEL_SERVICE_NAME="cloudops-server"`
    - `go run .`
- Generate one request:
  - `GET http://localhost:8080/ping`
  - or post one metric payload to `POST http://localhost:8080/api/v1/metrics`.
- Select service `cloudops-server` in Jaeger and search recent traces.

Kafka mode trace check:
- Start the server with Kafka mode and tracing:
  - `$env:INGEST_MODE="kafka"`
  - `$env:OTEL_ENABLED="true"`
  - `$env:OTEL_EXPORTER_OTLP_ENDPOINT="localhost:4317"`
  - `$env:OTEL_SERVICE_NAME="cloudops-server"`
  - `go run .`
- Start the worker in another terminal:
  - `$env:OTEL_ENABLED="true"`
  - `$env:OTEL_EXPORTER_OTLP_ENDPOINT="localhost:4317"`
  - `$env:OTEL_SERVICE_NAME="cloudops-worker"`
  - `$env:WORKER_METRICS_PORT="9091"`
  - `go run ./cmd/worker`
- Post one metric payload to `POST http://localhost:8080/api/v1/metrics`.
- In Jaeger, search `cloudops-server` or `cloudops-worker`; the same trace
  should include server publish, worker processing, worker forwarding, and the
  server `/api/v1/metrics/process` span.

If no traces appear:
- Check the collector: `docker logs cloudops-otel-collector`.
- Check Jaeger: `docker logs cloudops-jaeger`.
- Confirm `OTEL_ENABLED=true` is set before starting the server.

## 10) Kubernetes MVP
Build local images:
- `docker build -t cloudops/server:dev -f Dockerfile.server .`
- `docker build -t cloudops/worker:dev -f Dockerfile.worker .`
- `docker build -t cloudops/agent:dev -f agent/Dockerfile.agent ./agent`
- `docker build -t cloudops/assistant:dev -f Dockerfile.assistant .`
- `docker build -t cloudops/operator:dev -f Dockerfile.operator .`

For kind clusters, load images:
- `kind load docker-image cloudops/server:dev`
- `kind load docker-image cloudops/worker:dev`
- `kind load docker-image cloudops/agent:dev`
- `kind load docker-image cloudops/assistant:dev`
- `kind load docker-image cloudops/operator:dev`

Apply manifests:
- `kubectl apply -f deployments/k8s`
- `kubectl get pods -n cloudops`

Port-forward useful UIs:
- Server: `kubectl -n cloudops port-forward svc/cloudops-server 8080:8080`
- Grafana: `kubectl -n cloudops port-forward svc/cloudops-grafana 3000:3000`
- Jaeger: `kubectl -n cloudops port-forward svc/cloudops-jaeger 16686:16686`
- VictoriaMetrics: `kubectl -n cloudops port-forward svc/cloudops-victoriametrics 8428:8428`
- Worker metrics: `kubectl -n cloudops port-forward svc/cloudops-worker 9091:9091`
- Assistant: `kubectl -n cloudops port-forward svc/cloudops-assistant 8090:8090`

Kubernetes validation:
- `GET http://localhost:8080/ping`
- `GET http://localhost:8080/metrics`
- `GET http://localhost:9091/metrics`
- `GET http://localhost:8090/healthz`
- Grafana: `http://localhost:3000`
- Jaeger: `http://localhost:16686`
- VictoriaMetrics UI: `http://localhost:8428/vmui`

Assistant triage:
- PowerShell:
  - `Invoke-RestMethod -Method Post -Uri http://localhost:8090/api/v1/assistant/triage -ContentType "application/json" -Body '{"machine_id":"<machine-id>","window_minutes":15}'`

Notes:
- `deployments/k8s/secret.example.yaml` contains demo credentials only.
- The first K8s slice uses plain YAML and single-replica demo dependencies.
- The agent DaemonSet currently collects from its container/node view and is
  intended for demo validation before deeper Kubernetes metadata collection.

## 11) Prometheus Operator
Install kube-prometheus-stack first. Example with Helm:
- `helm repo add prometheus-community https://prometheus-community.github.io/helm-charts`
- `helm repo update`
- `helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack -n monitoring --create-namespace`

Apply CloudOps monitoring CRDs after the stack is ready:
- `kubectl apply -f deployments/k8s/monitoring`

Validation:
- Port-forward Prometheus from your kube-prometheus-stack release.
- Confirm `cloudops-server` and `cloudops-worker` ServiceMonitor targets are up.
- Confirm `CloudOpsServerIngestErrors`, `CloudOpsVictoriaMetricsWriteErrors`,
  `CloudOpsWorkerForwardErrors`, and `CloudOpsAlertNotificationFailures` are
  loaded as Prometheus rules.

## 12) CloudOpsMonitor CRD MVP
Apply the CRD and sample custom resource:
- `kubectl apply -f deployments/k8s/operator/cloudopsmonitor-crd.yaml`
- `kubectl apply -f deployments/k8s/operator/operator-deployment.yaml`
- `kubectl apply -f deployments/k8s/operator/cloudopsmonitor-sample.yaml`
- `kubectl get cloudopsmonitors -n cloudops`

Validate operator reconciliation:
- `kubectl logs -n cloudops -l app.kubernetes.io/name=cloudops-operator --tail=100`
- `kubectl get cloudopsmonitors -n cloudops`
- `kubectl patch cloudopsmonitor cloudops -n cloudops --type merge -p '{"spec":{"worker":{"replicas":2,"image":"cloudops/worker:dev"}}}'`
- `kubectl get deployment cloudops-worker -n cloudops`

The operator MVP reconciles:
- `cloudops-config` values for ingest mode and dependency endpoints.
- `cloudops-server` image and replica count.
- `cloudops-worker` image and replica count.
- `cloudops-agent` image.
