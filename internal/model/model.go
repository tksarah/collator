package model

import "time"

const RewardWallet = "WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN"

type NodeState struct {
	Name         string `json:"name"`
	ServiceState string `json:"service_state"`
	Version      string `json:"version"`
	Uptime       int64  `json:"uptime_seconds"`
	RestartCount int64  `json:"restart_count"`
}

type ChainState struct {
	LocalFinalized int64  `json:"local_finalized"`
	LocalBest      int64  `json:"local_best"`
	ExternalHeight int64  `json:"external_height"`
	ExternalA      int64  `json:"external_a"`
	ExternalB      int64  `json:"external_b"`
	Lag            int64  `json:"lag"`
	Peers          int64  `json:"peers"`
	Status         string `json:"status"`
}

type HostState struct {
	CPU         float64 `json:"cpu_percent"`
	Memory      float64 `json:"memory_percent"`
	Disk        float64 `json:"disk_percent"`
	Temperature float64 `json:"temperature_c"`
}

type AutomationState struct {
	Mode       string    `json:"mode"`
	Enabled    bool      `json:"enabled"`
	HostLocked bool      `json:"host_locked"`
	EligibleAt time.Time `json:"eligible_at"`
}

type Incident struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Severity    string    `json:"severity"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	OpenedAt    time.Time `json:"opened_at"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	Diagnosis   string    `json:"diagnosis,omitempty"`
	Evidence    any       `json:"evidence,omitempty"`
}

type Overview struct {
	Node       NodeState       `json:"node"`
	Chain      ChainState      `json:"chain"`
	Host       HostState       `json:"host"`
	Automation AutomationState `json:"automation"`
	Rewards    RewardOverview  `json:"rewards"`
	Incidents  []Incident      `json:"incidents"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// RewardOverview contains only public, read-only chain data. Planck amounts are
// strings so JSON consumers never lose precision converting uint128 values.
type RewardOverview struct {
	Address               string            `json:"address"`
	Status                string            `json:"status"`
	MonitoringStartedAt   time.Time         `json:"monitoring_started_at,omitempty"`
	ActiveSession         bool              `json:"active_session"`
	ValidatorCount        int               `json:"validator_count"`
	FinalizedBlock        int64             `json:"finalized_block"`
	LastScannedBlock      int64             `json:"last_scanned_block"`
	LastAuthoredBlock     int64             `json:"last_authored_block"`
	LastRewardAt          time.Time         `json:"last_reward_at,omitempty"`
	SecondsSinceReward    int64             `json:"seconds_since_reward"`
	BlocksSinceAuthored   int64             `json:"blocks_since_authored"`
	KickBlocksRemaining   int64             `json:"kick_blocks_remaining"`
	WalletFreePlanck      string            `json:"wallet_free_planck"`
	LastRewardPlanck      string            `json:"last_reward_planck"`
	Reward24hPlanck       string            `json:"reward_24h_planck"`
	Reward24hCount        int64             `json:"reward_24h_count"`
	RewardTotalPlanck     string            `json:"reward_total_planck"`
	RewardTotalCount      int64             `json:"reward_total_count"`
	SpecVersion           int64             `json:"spec_version"`
	SchemaOK              bool              `json:"schema_ok"`
	Quorum                int               `json:"quorum"`
	InactiveConfirmations int               `json:"inactive_confirmations"`
	Sources               map[string]string `json:"sources"`
	Gap                   string            `json:"gap,omitempty"`
}

type RewardObservation struct {
	BlockNumber        int64          `json:"block_number"`
	BlockHash          string         `json:"block_hash"`
	AuthoredAt         time.Time      `json:"authored_at"`
	ExpectedPlanck     string         `json:"expected_planck"`
	CreditedPlanck     string         `json:"credited_planck"`
	WalletBeforePlanck string         `json:"wallet_before_planck"`
	WalletAfterPlanck  string         `json:"wallet_after_planck"`
	PotBeforePlanck    string         `json:"pot_before_planck"`
	Verification       string         `json:"verification"`
	SourceCount        int            `json:"source_count"`
	Evidence           map[string]any `json:"evidence,omitempty"`
}

type RewardSnapshot struct {
	Address              string    `json:"address"`
	FinalizedBlock       int64     `json:"finalized_block"`
	FinalizedHash        string    `json:"finalized_hash"`
	LastAuthoredBlock    int64     `json:"last_authored_block"`
	LastAuthoredAt       time.Time `json:"last_authored_at,omitempty"`
	WalletFreePlanck     string    `json:"wallet_free_planck"`
	WalletReservedPlanck string    `json:"wallet_reserved_planck"`
	ActiveSession        bool      `json:"active_session"`
	ValidatorCount       int       `json:"validator_count"`
	SpecVersion          int64     `json:"spec_version"`
	SchemaOK             bool      `json:"schema_ok"`
	Error                string    `json:"error,omitempty"`
	Source               string    `json:"source"`
	ObservedAt           time.Time `json:"observed_at"`
}

type RewardScan struct {
	From         int64               `json:"from"`
	To           int64               `json:"to"`
	LastAuthored int64               `json:"last_authored"`
	Observations []RewardObservation `json:"observations"`
	Source       string              `json:"source"`
}

type RewardPage struct {
	Summary         RewardOverview      `json:"summary"`
	Items           []RewardObservation `json:"items"`
	Daily           []RewardDaily       `json:"daily"`
	NextBeforeBlock int64               `json:"next_before_block,omitempty"`
}

type RewardDaily struct {
	Day          string `json:"day"`
	AmountPlanck string `json:"amount_planck"`
	Count        int64  `json:"count"`
}

type Diagnosis struct {
	ID                string   `json:"id,omitempty"`
	IncidentID        string   `json:"incident_id,omitempty"`
	Severity          string   `json:"severity"`
	Summary           string   `json:"diagnosis"`
	EvidenceIDs       []string `json:"evidence_ids"`
	RecommendedAction string   `json:"recommended_action"`
	Confidence        float64  `json:"confidence"`
	OperatorSteps     []string `json:"operator_steps"`
	Model             string   `json:"model,omitempty"`
}

type LogLine struct {
	Cursor    string    `json:"cursor"`
	Timestamp time.Time `json:"timestamp"`
	Priority  string    `json:"priority"`
	Message   string    `json:"message"`
}

type AgentSnapshot struct {
	Node             NodeState `json:"node"`
	Host             HostState `json:"host"`
	MetricsOK        bool      `json:"metrics_ok"`
	AutomationLocked bool      `json:"automation_locked"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type RemediationAction struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	IncidentID     string    `json:"incident_id,omitempty"`
	RequestedBy    string    `json:"requested_by"`
	Reason         string    `json:"reason"`
	Mode           string    `json:"mode"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

type AuditEvent struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Result    string    `json:"result"`
	Details   any       `json:"details,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
