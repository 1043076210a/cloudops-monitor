# CloudOps 云原生监控告警平台

基于 Go 实现的云原生监控告警平台 MVP，覆盖主机指标采集、Direct/Kafka 双模式接入、Worker 异步消费、时序指标存储、告警状态收敛、Webhook 通知、可观测性、Kubernetes 部署、自定义 Operator 和只读型 AIOps 运维助手。

> 该项目用于云原生监控告警链路学习、演示和简历项目展示，不是生产级监控系统。

## 技术栈

- Go、Gin、GORM
- MySQL、Redis
- Kafka-compatible Redpanda
- VictoriaMetrics
- Prometheus、Grafana
- OpenTelemetry Collector、Jaeger
- Loki、Promtail
- Docker Compose、Kubernetes、kind
- k6
- OpenAI-compatible LLM / Ollama 可选接入

## 架构概览

```text
Agent
  -> Server /api/v1/metrics
      -> Direct 模式：直接处理指标
      -> Kafka 模式：发布到 Redpanda
            -> Worker 消费指标消息
                -> Server /api/v1/metrics/process
                    -> VictoriaMetrics 写入时序指标
                    -> MySQL 持久化告警事件
                    -> Redis 维护告警状态和 cooldown
                    -> Webhook 发送告警/恢复通知

Server / Worker
  -> /metrics 暴露平台自身 Prometheus 指标
  -> OpenTelemetry Collector -> Jaeger

Kubernetes 日志
  -> Promtail -> Loki -> Grafana

Assistant
  -> 查询最近告警、生成指标/日志查询链接
  -> 可选调用 OpenAI-compatible LLM
  -> 返回结构化诊断建议
```

## 核心能力

- Agent 自动注册主机信息，并按周期上报 CPU、内存、磁盘指标。
- Server 支持 Direct 和 Kafka/Redpanda 双接入模式。
- Worker 消费 Kafka 指标消息并转发到统一处理接口，实现接入层与处理层解耦。
- VictoriaMetrics 存储主机时序指标。
- MySQL 持久化机器元数据和告警事件。
- Redis 管理告警状态和 cooldown TTL，降低重复告警频率。
- Webhook 通知支持告警触发和恢复通知。
- Server 和 Worker 暴露 Prometheus-compatible `/metrics`。
- OpenTelemetry Trace 覆盖接入、Kafka 发布/消费、Worker 转发、VictoriaMetrics 写入等链路。
- Loki/Promtail/Grafana 提供日志采集和查询能力。
- Kubernetes YAML 覆盖 Server、Agent、Worker、MySQL、Redis、Redpanda、VictoriaMetrics、Grafana、Loki、Promtail、Jaeger、OTel Collector 等组件。
- `CloudOpsMonitor` 自定义 Operator MVP 可管理部分运行配置、镜像和副本数。
- Assistant 支持只读诊断；开启 LLM 后可基于告警、指标查询、日志查询和排查建议生成结构化 AIOps 诊断结果。

## 目录结构

```text
.
|-- agent/                         # 主机指标采集 Agent
|-- cmd/
|   |-- assistant/                  # 只读型运维诊断助手
|   |-- operator/                   # CloudOpsMonitor 自定义 Operator MVP
|   `-- worker/                     # Kafka 消费与转发 Worker
|-- deployments/
|   |-- docker-compose.yml          # 本地依赖栈
|   |-- grafana/                    # Grafana 数据源和仪表盘
|   `-- k8s/                        # Kubernetes YAML
|-- docs/                           # 演进路线和运行手册
|-- internal/ingest/publisher/      # Kafka 发布抽象
`-- tests/load/                     # k6 压测脚本
```

## 本地 Docker Compose 启动

启动依赖组件：

```powershell
cd deployments
docker compose up -d
```

启动 Server：

```powershell
cd ..
go run .
```

另开终端启动 Agent：

```powershell
cd agent
go run .
```

验证接口：

```powershell
Invoke-RestMethod http://localhost:8080/ping
Invoke-RestMethod http://localhost:8080/healthz/deps
```

常用地址：

- Server metrics: `http://localhost:8080/metrics`
- Worker metrics: `http://localhost:9091/metrics`
- VictoriaMetrics UI: `http://localhost:8428/vmui`
- Grafana: `http://localhost:3000`，演示账号 `admin/admin`
- Jaeger: `http://localhost:16686`

## Kubernetes kind 演示

构建镜像：

```powershell
docker build -t cloudops/server:dev -f Dockerfile.server .
docker build -t cloudops/worker:dev -f Dockerfile.worker .
docker build -t cloudops/assistant:dev -f Dockerfile.assistant .
docker build -t cloudops/operator:dev -f Dockerfile.operator .
docker build -t cloudops/agent:dev -f agent/Dockerfile.agent ./agent
```

创建 kind 集群并加载镜像：

```powershell
kind create cluster --name cloudops
kind load docker-image cloudops/server:dev --name cloudops
kind load docker-image cloudops/worker:dev --name cloudops
kind load docker-image cloudops/assistant:dev --name cloudops
kind load docker-image cloudops/operator:dev --name cloudops
kind load docker-image cloudops/agent:dev --name cloudops
```

部署：

```powershell
kubectl apply -f deployments/k8s/namespace.yaml
kubectl apply -f deployments/k8s
```

查看状态：

```powershell
kubectl get pods -n cloudops
kubectl get svc -n cloudops
```

端口转发 Server：

```powershell
kubectl -n cloudops port-forward svc/cloudops-server 8080:8080
```

## k6 压测

基础功能压测：

```powershell
k6 run tests/load/metrics_ingest_k6.js
```

固定 RPS 压测脚本：

```text
tests/load/metrics_ingest_rate_k6.js
```

阶梯升压脚本：

```text
tests/load/metrics_ingest_ramp_k6.js
```

推荐在 Kubernetes 集群内部运行 k6，避免 `kubectl port-forward` 成为瓶颈。示例：1000 req/s，持续 60s。

```powershell
Get-Content .\tests\load\metrics_ingest_rate_k6.js |
  kubectl -n cloudops run k6-rate-1000 --rm -i --restart=Never --image=grafana/k6 `
  --env="BASE_URL=http://cloudops-server:8080" `
  --env="RATE=1000" `
  --env="DURATION=60s" `
  --env="PRE_ALLOCATED_VUS=500" `
  --env="MAX_VUS=2000" `
  --command -- k6 run -
```

本地 kind 环境中的一次压测结果：

```text
1000 req/s，持续 60s
60000 次请求
HTTP 失败率 0%
P95 延迟约 11ms
```

Kafka 模式下还需要通过 Prometheus 指标确认完整链路：

```powershell
(Invoke-WebRequest http://localhost:8080/metrics -UseBasicParsing).Content | Select-String "cloudops_server"
(Invoke-WebRequest http://localhost:9091/metrics -UseBasicParsing).Content | Select-String "cloudops_worker"
```

重点确认：

```text
Server accepted
Kafka publish success
Worker read
Worker forward success
Server process success
VictoriaMetrics write success
```

## AIOps 运维助手

Assistant 是只读型诊断服务，不执行命令，不修改 Kubernetes 资源。默认返回规则型诊断建议；当 `LLM_ENABLED=true` 时，会调用 OpenAI-compatible Chat Completions 接口，并返回结构化 `diagnosis` 字段。

端口转发：

```powershell
kubectl -n cloudops port-forward svc/cloudops-assistant 8090:8090
```

调用：

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8090/api/v1/assistant/triage `
  -ContentType "application/json" `
  -Body '{"machine_id":"k6-rate-node-1","window_minutes":15}'
```

OpenAI-compatible 示例配置：

```text
LLM_ENABLED=true
LLM_BASE_URL=https://api.deepseek.com
LLM_API_KEY=<your-api-key>
LLM_MODEL=deepseek-chat
LLM_TIMEOUT_SECONDS=15
```

Ollama OpenAI-compatible 示例配置：

```text
LLM_ENABLED=true
LLM_BASE_URL=http://localhost:11434
LLM_MODEL=qwen2.5:7b
LLM_API_KEY=
```

期望的 LLM 结构化输出：

```json
{
  "summary": "CPU 使用率持续超过阈值",
  "severity": "warning",
  "possible_causes": ["业务流量突增", "CPU limit 偏低"],
  "evidence": ["最近存在 CPU P0 告警", "已生成指标和日志查询链接"],
  "next_steps": ["查看 Loki 日志", "查看 Jaeger Trace", "确认最近发布记录"],
  "confidence": 0.72
}
```

## 自定义 Operator MVP

部署 CRD 和 Operator：

```powershell
kubectl apply -f deployments/k8s/operator/cloudopsmonitor-crd.yaml
kubectl apply -f deployments/k8s/operator/operator-deployment.yaml
kubectl apply -f deployments/k8s/operator/cloudopsmonitor-sample.yaml
```

验证：

```powershell
kubectl logs -n cloudops -l app.kubernetes.io/name=cloudops-operator --tail=100
kubectl get cloudopsmonitors -n cloudops
```

## Prometheus Operator 资源

`deployments/k8s/monitoring` 下提供了 `ServiceMonitor` 和 `PrometheusRule`。这部分需要先安装 Prometheus Operator CRD，例如使用 `kube-prometheus-stack`：

```powershell
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack -n monitoring --create-namespace
kubectl apply -f deployments/k8s/monitoring
```

该步骤对基础演示不是必需项。

## 配置与安全说明

- `.env.example` 提供本地开发示例配置。
- `deployments/k8s/secret.example.yaml` 只包含演示用途的默认值。
- 不要提交真实 Webhook 地址、API Key、Token、数据库密码或 kubeconfig。
- 本地演示默认 `NOTIFIER_TYPE=noop`，不会发送真实 Webhook。
- LLM API Key 应通过环境变量或 Kubernetes Secret 注入，不应写入代码或 ConfigMap。

## 开发检查

```powershell
go test ./...
go build .
go build ./cmd/worker
go build ./cmd/assistant
go build ./cmd/operator
```

Agent：

```powershell
cd agent
go test ./...
go build .
```

## License

如需允许他人复用代码，建议补充 MIT License 或其他开源许可证。
