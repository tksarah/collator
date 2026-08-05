package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

type Config struct {
	DatabaseURL         string
	ListenAddress       string
	CookieSecure        bool
	BootstrapToken      string
	EncryptionKey       string
	AgentObserveSock    string
	AgentControlSock    string
	ActionBrokerSock    string
	ActionBrokerKey     string
	NodeName            string
	SystemdUnit         string
	GeminiAPIKey        string
	GeminiModel         string
	SMTPHost            string
	SMTPPort            int
	SMTPUser            string
	SMTPPassword        string
	SMTPFrom            string
	SMTPTo              []string
	SMTPUnixSocket      string
	SMTPTLSMode         string
	ExternalRPC         []string
	RewardAddress       string
	RewardWarnAfter     time.Duration
	RewardCriticalAfter time.Duration
	PrometheusURL       string
	InstalledAt         time.Time
	ObserveOnlyPeriod   time.Duration
}

func Load() (Config, error) {
	installedAt, _ := time.Parse(time.RFC3339, getenv("INSTALLED_AT", time.Now().UTC().Format(time.RFC3339)))
	cfg := Config{
		DatabaseURL:         secret("DATABASE_URL", ""),
		ListenAddress:       getenv("LISTEN_ADDRESS", ":8080"),
		CookieSecure:        getenv("COOKIE_SECURE", "true") == "true",
		BootstrapToken:      secret("BOOTSTRAP_TOKEN", ""),
		EncryptionKey:       secret("ENCRYPTION_KEY", ""),
		AgentObserveSock:    getenv("AGENT_OBSERVE_SOCKET", "/run/shiden-guardian/observe/agent.sock"),
		AgentControlSock:    getenv("AGENT_CONTROL_SOCKET", "/run/shiden-guardian/control/agent.sock"),
		ActionBrokerSock:    getenv("ACTION_BROKER_SOCKET", "/run/shiden-guardian/action-broker/controller.sock"),
		ActionBrokerKey:     secret("ACTION_BROKER_KEY", ""),
		NodeName:            getenv("NODE_NAME", "tk_sdn_collator"),
		SystemdUnit:         getenv("SYSTEMD_UNIT", "astar.service"),
		GeminiAPIKey:        secret("GEMINI_API_KEY", ""),
		GeminiModel:         getenv("GEMINI_MODEL", "gemini-3.6-flash"),
		SMTPHost:            getenv("SMTP_HOST", ""),
		SMTPPort:            intenv("SMTP_PORT", 587),
		SMTPUser:            getenv("SMTP_USER", ""),
		SMTPPassword:        secret("SMTP_PASSWORD", ""),
		SMTPFrom:            getenv("SMTP_FROM", ""),
		SMTPTo:              split(getenv("SMTP_TO", "")),
		SMTPUnixSocket:      getenv("SMTP_UNIX_SOCKET", ""),
		SMTPTLSMode:         getenv("SMTP_TLS_MODE", "required"),
		ExternalRPC:         split(getenv("EXTERNAL_RPC_URLS", "https://shiden-rpc.n.dwellir.com,https://shiden.api.onfinality.io/public")),
		RewardAddress:       getenv("COLLATOR_REWARD_ADDRESS", model.RewardWallet),
		RewardWarnAfter:     time.Duration(intenv("REWARD_WARNING_MINUTES", 15)) * time.Minute,
		RewardCriticalAfter: time.Duration(intenv("REWARD_CRITICAL_MINUTES", 30)) * time.Minute,
		PrometheusURL:       getenv("PROMETHEUS_URL", "http://prometheus:9090"),
		InstalledAt:         installedAt,
		ObserveOnlyPeriod:   time.Duration(intenv("OBSERVE_ONLY_DAYS", 14)) * 24 * time.Hour,
	}
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.RewardAddress != model.RewardWallet {
		return cfg, fmt.Errorf("COLLATOR_REWARD_ADDRESS must be the configured collator wallet")
	}
	if len(cfg.ExternalRPC) < 2 {
		return cfg, fmt.Errorf("two EXTERNAL_RPC_URLS are required")
	}
	if cfg.RewardWarnAfter <= 0 || cfg.RewardCriticalAfter <= cfg.RewardWarnAfter {
		return cfg, fmt.Errorf("reward thresholds must be positive and critical must exceed warning")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func intenv(key string, fallback int) int {
	value, err := strconv.Atoi(getenv(key, ""))
	if err != nil {
		return fallback
	}
	return value
}
func split(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
func secret(key, fallback string) string {
	if path := strings.TrimSpace(os.Getenv(key + "_FILE")); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return getenv(key, fallback)
}
