package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type CloudOpsMonitorSpec struct {
	IngestMode   string           `json:"ingestMode"`
	Server       WorkloadSpec     `json:"server"`
	Worker       WorkloadSpec     `json:"worker"`
	Agent        AgentSpec        `json:"agent"`
	Dependencies DependenciesSpec `json:"dependencies"`
}

type WorkloadSpec struct {
	Image    string `json:"image"`
	Replicas int32  `json:"replicas"`
}

type AgentSpec struct {
	Image string `json:"image"`
}

type DependenciesSpec struct {
	MySQLDSNSecretRef  string `json:"mysqlDSNSecretRef"`
	RedisAddr          string `json:"redisAddr"`
	KafkaBrokers       string `json:"kafkaBrokers"`
	VictoriaMetricsURL string `json:"victoriaMetricsURL"`
	OTelEndpoint       string `json:"otelEndpoint"`
}

var cloudOpsMonitorGVR = schema.GroupVersionResource{
	Group:    "cloudops.example.com",
	Version:  "v1alpha1",
	Resource: "cloudopsmonitors",
}

func main() {
	namespace := getEnv("WATCH_NAMESPACE", "cloudops")
	name := getEnv("CLOUDOPS_MONITOR_NAME", "cloudops")
	interval := time.Duration(getEnvAsInt("RECONCILE_INTERVAL_SECONDS", 15)) * time.Second

	cfg, err := kubeConfig()
	if err != nil {
		log.Fatal(err)
	}
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("cloudops operator started namespace=%s resource=%s interval=%s", namespace, name, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := reconcile(ctx, kubeClient, dynamicClient, namespace, name); err != nil {
			log.Printf("reconcile failed: %v", err)
		}

		select {
		case <-ctx.Done():
			log.Println("cloudops operator stopped")
			return
		case <-ticker.C:
		}
	}
}

func reconcile(ctx context.Context, kubeClient kubernetes.Interface, dynamicClient dynamic.Interface, namespace, name string) error {
	resource, err := dynamicClient.Resource(cloudOpsMonitorGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	spec, err := parseSpec(resource)
	if err != nil {
		_ = patchStatus(ctx, dynamicClient, namespace, name, "Error", resource.GetGeneration(), err.Error())
		return err
	}

	if err := reconcileConfigMap(ctx, kubeClient, namespace, spec); err != nil {
		_ = patchStatus(ctx, dynamicClient, namespace, name, "Error", resource.GetGeneration(), err.Error())
		return err
	}
	if err := reconcileDeployment(ctx, kubeClient, namespace, "cloudops-server", "server", spec.Server.Image, spec.Server.Replicas); err != nil {
		_ = patchStatus(ctx, dynamicClient, namespace, name, "Error", resource.GetGeneration(), err.Error())
		return err
	}
	if err := reconcileDeployment(ctx, kubeClient, namespace, "cloudops-worker", "worker", spec.Worker.Image, spec.Worker.Replicas); err != nil {
		_ = patchStatus(ctx, dynamicClient, namespace, name, "Error", resource.GetGeneration(), err.Error())
		return err
	}
	if err := reconcileDaemonSet(ctx, kubeClient, namespace, "cloudops-agent", "agent", spec.Agent.Image); err != nil {
		_ = patchStatus(ctx, dynamicClient, namespace, name, "Error", resource.GetGeneration(), err.Error())
		return err
	}

	return patchStatus(ctx, dynamicClient, namespace, name, "Ready", resource.GetGeneration(), "workloads reconciled")
}

func parseSpec(resource *unstructured.Unstructured) (CloudOpsMonitorSpec, error) {
	raw, ok, err := unstructured.NestedMap(resource.Object, "spec")
	if err != nil {
		return CloudOpsMonitorSpec{}, err
	}
	if !ok {
		return CloudOpsMonitorSpec{}, fmt.Errorf("spec is missing")
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return CloudOpsMonitorSpec{}, err
	}
	var spec CloudOpsMonitorSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return CloudOpsMonitorSpec{}, err
	}
	if spec.Server.Replicas < 1 {
		spec.Server.Replicas = 1
	}
	if spec.Worker.Replicas < 0 {
		spec.Worker.Replicas = 0
	}
	return spec, nil
}

func reconcileConfigMap(ctx context.Context, kubeClient kubernetes.Interface, namespace string, spec CloudOpsMonitorSpec) error {
	cm, err := kubeClient.CoreV1().ConfigMaps(namespace).Get(ctx, "cloudops-config", metav1.GetOptions{})
	if err != nil {
		return err
	}
	ensureData(cm)
	cm.Data["INGEST_MODE"] = spec.IngestMode
	cm.Data["KAFKA_BROKERS"] = spec.Dependencies.KafkaBrokers
	cm.Data["REDIS_ADDR"] = spec.Dependencies.RedisAddr
	cm.Data["VICTORIA_METRICS_URL"] = spec.Dependencies.VictoriaMetricsURL
	if strings.TrimSpace(spec.Dependencies.OTelEndpoint) != "" {
		cm.Data["OTEL_EXPORTER_OTLP_ENDPOINT"] = spec.Dependencies.OTelEndpoint
	}
	_, err = kubeClient.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

func reconcileDeployment(ctx context.Context, kubeClient kubernetes.Interface, namespace, name, containerName, image string, replicas int32) error {
	deploy, err := kubeClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	deploy.Spec.Replicas = &replicas
	if image != "" {
		setDeploymentImage(deploy, containerName, image)
	}
	_, err = kubeClient.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return err
}

func reconcileDaemonSet(ctx context.Context, kubeClient kubernetes.Interface, namespace, name, containerName, image string) error {
	ds, err := kubeClient.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if image != "" {
		for i := range ds.Spec.Template.Spec.Containers {
			if ds.Spec.Template.Spec.Containers[i].Name == containerName {
				ds.Spec.Template.Spec.Containers[i].Image = image
			}
		}
	}
	_, err = kubeClient.AppsV1().DaemonSets(namespace).Update(ctx, ds, metav1.UpdateOptions{})
	return err
}

func setDeploymentImage(deploy *appsv1.Deployment, containerName, image string) {
	for i := range deploy.Spec.Template.Spec.Containers {
		if deploy.Spec.Template.Spec.Containers[i].Name == containerName {
			deploy.Spec.Template.Spec.Containers[i].Image = image
		}
	}
}

func patchStatus(ctx context.Context, dynamicClient dynamic.Interface, namespace, name, phase string, generation int64, message string) error {
	status := map[string]any{
		"status": map[string]any{
			"phase":              phase,
			"observedGeneration": generation,
			"message":            message,
		},
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	_, err = dynamicClient.Resource(cloudOpsMonitorGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{}, "status")
	return err
}

func ensureData(cm *corev1.ConfigMap) {
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
}

func kubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
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
