package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

const (
	defaultServerBaseURL = "http://localhost:8080"
	defaultReportEvery   = 10 * time.Second
)

type Metrics struct {
	MachineID string  `json:"machine_id"`
	IP        string  `json:"ip"`
	CPUUsage  float64 `json:"cpu_usage"`
	MemUsage  float64 `json:"mem_usage"`
	DiskUsage float64 `json:"disk_usage"`
	Timestamp int64   `json:"timestamp"`
}

type MachineRegisterPayload struct {
	MachineID string `json:"machine_id"`
	IP        string `json:"ip"`
	Hostname  string `json:"hostname"`
	Status    string `json:"status"`
}

type AgentIdentity struct {
	MachineID string
	IP        string
	Hostname  string
}

func main() {
	serverBaseURL := getEnv("SERVER_BASE_URL", defaultServerBaseURL)
	reportEvery := defaultReportEvery

	identity := detectIdentity()
	log.Printf("🧩 Agent身份识别: machine_id=%s hostname=%s ip=%s", identity.MachineID, identity.Hostname, identity.IP)

	registered := registerMachineWithRetry(serverBaseURL, identity, 5)
	if !registered {
		log.Printf("⚠️ 首次注册未成功，后续将继续重试，不影响指标上报")
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}
	nextRegisterRetryAt := time.Now().Add(30 * time.Second)

	// 保留现有采集循环，仅在循环前后增加身份和注册逻辑
	for {
		cpuPercent, _ := cpu.Percent(time.Second, false)
		memInfo, _ := mem.VirtualMemory()
		diskUsage := collectDiskUsage()
		if len(cpuPercent) == 0 {
			log.Printf("⚠️ 未采集到CPU数据，跳过本次上报")
			time.Sleep(reportEvery)
			continue
		}

		metrics := Metrics{
			MachineID: identity.MachineID,
			IP:        identity.IP,
			CPUUsage:  cpuPercent[0],
			MemUsage:  memInfo.UsedPercent,
			DiskUsage: diskUsage,
			Timestamp: time.Now().Unix(),
		}

		data, err := json.Marshal(metrics)
		if err != nil {
			log.Printf("❌ 序列化指标失败: %v", err)
			time.Sleep(reportEvery)
			continue
		}

		resp, err := httpClient.Post(serverBaseURL+"/api/v1/metrics", "application/json", bytes.NewBuffer(data))
		if err != nil {
			log.Printf("❌ 上报失败: %v", err)
		} else {
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Printf("⚠️ 上报返回非2xx: status=%d", resp.StatusCode)
			}
		}

		log.Printf("💻 Agent已发送数据: CPU=%.1f%%, 内存=%.1f%%, 磁盘=%.1f%%", metrics.CPUUsage, metrics.MemUsage, metrics.DiskUsage)

		if !registered && time.Now().After(nextRegisterRetryAt) {
			registered = registerMachineWithRetry(serverBaseURL, identity, 1)
			nextRegisterRetryAt = time.Now().Add(30 * time.Second)
		}

		time.Sleep(reportEvery)
	}
}

func collectDiskUsage() float64 {
	path := "/"
	if runtime.GOOS == "windows" {
		path = "C:\\"
	}
	usage, err := disk.Usage(path)
	if err != nil {
		return 0
	}
	return usage.UsedPercent
}

func detectIdentity() AgentIdentity {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown-host"
	}

	ip := detectLocalIPv4()
	mac := detectPrimaryMAC()
	machineID := buildMachineID(hostname, mac)

	return AgentIdentity{
		MachineID: machineID,
		IP:        ip,
		Hostname:  hostname,
	}
}

func detectLocalIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	for _, iface := range ifaces {
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip != nil {
				return ip.String()
			}
		}
	}

	return "127.0.0.1"
}

func detectPrimaryMAC() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range ifaces {
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
			continue
		}
		if strings.TrimSpace(iface.HardwareAddr.String()) != "" {
			return iface.HardwareAddr.String()
		}
	}
	return ""
}

func buildMachineID(hostname, mac string) string {
	h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(hostname), " ", "-"))
	m := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(mac), ":", ""))
	if m == "" {
		return h
	}
	return fmt.Sprintf("%s-%s", h, m)
}

func registerMachineWithRetry(serverBaseURL string, identity AgentIdentity, maxAttempts int) bool {
	payload := MachineRegisterPayload{
		MachineID: identity.MachineID,
		IP:        identity.IP,
		Hostname:  identity.Hostname,
		Status:    "active",
	}

	client := &http.Client{Timeout: 5 * time.Second}
	backoff := 1 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ok, err := registerOnce(client, serverBaseURL+"/api/v1/machines/register", payload)
		if err == nil && ok {
			log.Printf("✅ 机器注册成功: machine_id=%s", identity.MachineID)
			return true
		}

		log.Printf("⚠️ 机器注册失败(第%d/%d次): %v", attempt, maxAttempts, err)
		if attempt < maxAttempts {
			time.Sleep(backoff)
			if backoff < 8*time.Second {
				backoff *= 2
			}
		}
	}

	return false
}

func registerOnce(client *http.Client, registerURL string, payload MachineRegisterPayload) (bool, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	resp, err := client.Post(registerURL, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("register returned status=%d", resp.StatusCode)
	}
	return true, nil
}

func getEnv(key, defaultValue string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return defaultValue
}
