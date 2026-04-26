# CloudOps Monitor

A Go-based cloud-native monitoring and alerting platform MVP. It covers host metric collection, Kafka-based ingest decoupling, time-series storage, alert state convergence, webhook notification, observability, Kubernetes deployment, and a read-only operations assistant.

> This project is designed for learning, demos, and resume/project portfolio presentation. It is not a production-ready monitoring system.

## Tech Stack

- Go, Gin, GORM
- MySQL, Redis
- Kafka-compatible Redpanda
- VictoriaMetrics
- Prometheus metrics, Grafana dashboards
- OpenTelemetry Collector, Jaeger
- Loki, Promtail
- Docker Compose, Kubernetes, kind
- k6 load testing

## Architecture

```text
Agent
  -> Server /api/v1/metrics
      -> direct mode: process metrics
      -> kafka mode: publish to Redpanda
            -> Worker
                -> Server /api/v1/metrics/process
                    -> VictoriaMetrics
                    -> MySQL alert events
                    -> Redis alert state/cooldown
                    -> Webhook notification

Server/Worker
  -> /metrics for Prometheus-compatible platform metrics
  -> OpenTelemetry Collector -> Jaeger

Kubernetes logs
  -> Promtail -> Loki -> Grafana
```

## Features

- Agent auto-registers host metadata and reports CPU, memory, and disk usage.
- Server supports both direct ingest and Kafka/Redpanda decoupled ingest.
- Worker consumes metric events from Kafka and forwards them to a unified processing endpoint.
- VictoriaMetrics stores host time-series metrics.
- MySQL stores machine metadata and alert events.
- Redis tracks alert state and cooldown TTL to reduce duplicate notifications.
- Webhook notifier supports alert and recovery notification paths.
- Server and worker expose Prometheus-compatible platform metrics.
- OpenTelemetry tracing covers ingest, Kafka publish/consume, worker forwarding, and VictoriaMetrics write paths.
- Kubernetes manifests cover Server, Agent, Worker, MySQL, Redis, Redpanda, VictoriaMetrics, Grafana, Loki, Promtail, Jaeger, and OTel Collector.
- `CloudOpsMonitor` custom Operator MVP manages selected runtime configuration, images, and replicas.
- Read-only Assistant MVP returns alert context, metric query links, log query links, and diagnostic suggestions.

## Repository Layout

```text
.
├── agent/                         # host metric agent
├── cmd/
│   ├── assistant/                  # read-only operations assistant
│   ├── operator/                   # CloudOpsMonitor custom operator MVP
│   └── worker/                     # Kafka consumer and forwarder
├── deployments/
│   ├── docker-compose.yml          # local dependency stack
│   ├── grafana/                    # dashboards and datasource provisioning
│   └── k8s/                        # Kubernetes manifests
├── docs/                           # roadmap and runbook
├── internal/ingest/publisher/      # Kafka publisher abstraction
└── tests/load/                     # k6 load test scripts
```

## Quick Start With Docker Compose

Start dependencies:

```powershell
cd deployments
docker compose up -d
```

Start the server:

```powershell
cd ..
go run .
```

Start the agent in another terminal:

```powershell
cd agent
go run .
```

Verify:

```powershell
Invoke-RestMethod http://localhost:8080/ping
Invoke-RestMethod http://localhost:8080/healthz/deps
```

Useful local URLs:

- Server metrics: `http://localhost:8080/metrics`
- Worker metrics: `http://localhost:9091/metrics`
- VictoriaMetrics UI: `http://localhost:8428/vmui`
- Grafana: `http://localhost:3000` with demo login `admin/admin`
- Jaeger: `http://localhost:16686`

## Kubernetes Demo With kind

Build local images:

```powershell
docker build -t cloudops/server:dev -f Dockerfile.server .
docker build -t cloudops/worker:dev -f Dockerfile.worker .
docker build -t cloudops/assistant:dev -f Dockerfile.assistant .
docker build -t cloudops/operator:dev -f Dockerfile.operator .
docker build -t cloudops/agent:dev -f agent/Dockerfile.agent ./agent
```

Create a kind cluster and load images:

```powershell
kind create cluster --name cloudops
kind load docker-image cloudops/server:dev --name cloudops
kind load docker-image cloudops/worker:dev --name cloudops
kind load docker-image cloudops/assistant:dev --name cloudops
kind load docker-image cloudops/operator:dev --name cloudops
kind load docker-image cloudops/agent:dev --name cloudops
```

Apply manifests:

```powershell
kubectl apply -f deployments/k8s/namespace.yaml
kubectl apply -f deployments/k8s
```

Check status:

```powershell
kubectl get pods -n cloudops
kubectl get svc -n cloudops
```

Port-forward the server:

```powershell
kubectl -n cloudops port-forward svc/cloudops-server 8080:8080
```

## Load Testing

The k6 script posts synthetic host metrics to `/api/v1/metrics`.

```powershell
k6 run tests/load/metrics_ingest_k6.js
```

For Docker-based k6:

```powershell
Get-Content .\tests\load\metrics_ingest_k6.js |
  docker run --rm -i -e BASE_URL=http://host.docker.internal:8080 grafana/k6 run -
```

Sample local kind result:

```text
20 VUs, 30s
2800 requests
~92.8 req/s
0% HTTP failures
P95 latency ~18ms
```

In Kafka mode, verify the full path through Prometheus metrics:

```powershell
(Invoke-WebRequest http://localhost:8080/metrics -UseBasicParsing).Content | Select-String "cloudops_server"
(Invoke-WebRequest http://localhost:9091/metrics -UseBasicParsing).Content | Select-String "cloudops_worker"
```

Expected evidence includes successful Kafka publish, worker consume/forward, server process, and VictoriaMetrics write counters.

## Operations Assistant

The assistant is a read-only MVP. It does not execute commands or modify Kubernetes resources.

```powershell
kubectl -n cloudops port-forward svc/cloudops-assistant 8090:8090
```

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8090/api/v1/assistant/triage `
  -ContentType "application/json" `
  -Body '{"machine_id":"k6-load-node","window_minutes":15}'
```

## Custom Operator MVP

Apply the CRD and operator:

```powershell
kubectl apply -f deployments/k8s/operator/cloudopsmonitor-crd.yaml
kubectl apply -f deployments/k8s/operator/operator-deployment.yaml
kubectl apply -f deployments/k8s/operator/cloudopsmonitor-sample.yaml
```

Validate reconciliation:

```powershell
kubectl logs -n cloudops -l app.kubernetes.io/name=cloudops-operator --tail=100
kubectl get cloudopsmonitors -n cloudops
```

## Prometheus Operator Resources

`deployments/k8s/monitoring` contains `ServiceMonitor` and `PrometheusRule` resources. They require Prometheus Operator CRDs, for example from `kube-prometheus-stack`.

```powershell
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack -n monitoring --create-namespace
kubectl apply -f deployments/k8s/monitoring
```

This step is optional for the basic demo.

## Configuration And Secrets

- `.env.example` contains local development defaults.
- `deployments/k8s/secret.example.yaml` contains demo-only Kubernetes secret values.
- Do not commit real webhook URLs, API keys, tokens, database passwords, or kubeconfig files.
- For local demos, `NOTIFIER_TYPE=noop` disables external webhook delivery.

## Development Checks

```powershell
go test ./...
go build .
go build ./cmd/worker
go build ./cmd/assistant
go build ./cmd/operator
```

Agent checks:

```powershell
cd agent
go test ./...
go build .
```

## License

Add a license before publishing if you want others to reuse the code.
