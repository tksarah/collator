package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shiden-guardian/shiden-guardian/internal/auth"
	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(834733101)`); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	for _, statement := range migrationStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}
	return tx.Commit()
}

func migrationStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS users (id text PRIMARY KEY, username text UNIQUE NOT NULL, password_hash text NOT NULL, totp_secret text NOT NULL, recovery_hashes jsonb NOT NULL DEFAULT '[]', active boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS sessions (token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, csrf_hash text NOT NULL, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS runtime_state (id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1), payload jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS incidents (id text PRIMARY KEY, fingerprint text NOT NULL, severity text NOT NULL, title text NOT NULL, status text NOT NULL DEFAULT 'open', evidence jsonb NOT NULL DEFAULT '{}', diagnosis text NOT NULL DEFAULT '', opened_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), resolved_at timestamptz)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS incidents_open_fingerprint_idx ON incidents(fingerprint) WHERE status='open'`,
		`CREATE TABLE IF NOT EXISTS diagnoses (id text PRIMARY KEY, incident_id text REFERENCES incidents(id) ON DELETE CASCADE, payload jsonb NOT NULL, model text NOT NULL, prompt_version text NOT NULL, created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS remediation_actions (id text PRIMARY KEY, idempotency_key text UNIQUE NOT NULL, incident_id text, requested_by text NOT NULL, reason text NOT NULL, mode text NOT NULL, status text NOT NULL DEFAULT 'pending', result jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz)`,
		`CREATE INDEX IF NOT EXISTS remediation_pending_idx ON remediation_actions(status, created_at)`,
		`CREATE TABLE IF NOT EXISTS settings (key text PRIMARY KEY, value jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS audit_events (id text PRIMARY KEY, action text NOT NULL, actor text NOT NULL, result text NOT NULL, details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE INDEX IF NOT EXISTS audit_created_idx ON audit_events(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS jobs (id text PRIMARY KEY, kind text NOT NULL, payload jsonb NOT NULL DEFAULT '{}', status text NOT NULL DEFAULT 'pending', created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz)`,
		`CREATE TABLE IF NOT EXISTS reward_events (block_number bigint PRIMARY KEY, block_hash text UNIQUE NOT NULL, authored_at timestamptz NOT NULL, expected_planck numeric(39,0) NOT NULL, credited_planck numeric(39,0) NOT NULL, wallet_before_planck numeric(39,0) NOT NULL, wallet_after_planck numeric(39,0) NOT NULL, pot_before_planck numeric(39,0) NOT NULL, verification text NOT NULL, source_count smallint NOT NULL DEFAULT 1, evidence jsonb NOT NULL DEFAULT '{}', detected_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE INDEX IF NOT EXISTS reward_events_authored_idx ON reward_events(authored_at DESC)`,
		`CREATE TABLE IF NOT EXISTS reward_monitor_state (id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1), monitoring_started_at timestamptz NOT NULL, last_scanned_block bigint NOT NULL, payload jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())`,
	}
}

func DryRunMigrations(ctx context.Context, url string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(834733101)`); err != nil {
		return fmt.Errorf("migration dry-run lock: %w", err)
	}
	for _, statement := range migrationStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration dry-run: %w", err)
		}
	}
	return tx.Rollback()
}

func ID(prefix string) string { token, _ := auth.RandomToken(12); return prefix + "_" + token }
func JSON(value any) []byte   { data, _ := json.Marshal(value); return data }

func (s *Store) SaveOverview(ctx context.Context, overview model.Overview) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO runtime_state(id,payload,updated_at) VALUES(1,$1,now()) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,updated_at=now()`, JSON(overview))
	return err
}
func (s *Store) LoadOverview(ctx context.Context) (model.Overview, error) {
	var data []byte
	var overview model.Overview
	err := s.DB.QueryRowContext(ctx, `SELECT payload FROM runtime_state WHERE id=1`).Scan(&data)
	if err != nil {
		return overview, err
	}
	err = json.Unmarshal(data, &overview)
	return overview, err
}

func (s *Store) ListIncidents(ctx context.Context, limit int) ([]model.Incident, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,fingerprint,severity,title,status,opened_at,updated_at,diagnosis,evidence FROM incidents ORDER BY opened_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Incident{}
	for rows.Next() {
		var x model.Incident
		var evidence []byte
		if err := rows.Scan(&x.ID, &x.Fingerprint, &x.Severity, &x.Title, &x.Status, &x.OpenedAt, &x.UpdatedAt, &x.Diagnosis, &evidence); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &x.Evidence)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) OpenIncident(ctx context.Context, fingerprint, severity, title string, evidence any) (model.Incident, bool, error) {
	x := model.Incident{ID: ID("inc"), Fingerprint: fingerprint, Severity: severity, Title: title, Status: "open", OpenedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Evidence: evidence}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO incidents(id,fingerprint,severity,title,evidence) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, x.ID, fingerprint, severity, title, JSON(evidence))
	if err != nil {
		return x, false, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		err = s.DB.QueryRowContext(ctx, `SELECT id,severity,title,status,opened_at,updated_at,diagnosis FROM incidents WHERE fingerprint=$1 AND status='open'`, fingerprint).Scan(&x.ID, &x.Severity, &x.Title, &x.Status, &x.OpenedAt, &x.UpdatedAt, &x.Diagnosis)
		return x, false, err
	}
	return x, true, nil
}
func (s *Store) SetDiagnosis(ctx context.Context, incidentID string, diagnosis model.Diagnosis) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	diagnosis.ID = ID("diag")
	diagnosis.IncidentID = incidentID
	if _, err = tx.ExecContext(ctx, `INSERT INTO diagnoses(id,incident_id,payload,model,prompt_version) VALUES($1,$2,$3,$4,'v1')`, diagnosis.ID, incidentID, JSON(diagnosis), diagnosis.Model); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE incidents SET diagnosis=$2,updated_at=now() WHERE id=$1`, incidentID, diagnosis.Summary); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Audit(ctx context.Context, action, actor, result string, details any) {
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO audit_events(id,action,actor,result,details) VALUES($1,$2,$3,$4,$5)`, ID("aud"), action, actor, result, JSON(details))
}
func (s *Store) ListAudit(ctx context.Context, limit int) ([]model.AuditEvent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,action,actor,result,details,created_at FROM audit_events ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEvent{}
	for rows.Next() {
		var x model.AuditEvent
		var raw []byte
		if err := rows.Scan(&x.ID, &x.Action, &x.Actor, &x.Result, &raw, &x.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &x.Details)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) SettingBool(ctx context.Context, key string, fallback bool) bool {
	var raw []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw); err != nil {
		return fallback
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return fallback
	}
	return value
}
func (s *Store) SetSetting(ctx context.Context, key string, value any) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,now()) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=now()`, key, JSON(value))
	return err
}

func (s *Store) SaveRewardOverview(ctx context.Context, overview model.RewardOverview) error {
	started := overview.MonitoringStartedAt
	if started.IsZero() {
		started = time.Now().UTC()
		overview.MonitoringStartedAt = started
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO reward_monitor_state(id,monitoring_started_at,last_scanned_block,payload,updated_at) VALUES(1,$1,$2,$3,now()) ON CONFLICT(id) DO UPDATE SET last_scanned_block=excluded.last_scanned_block,payload=excluded.payload,updated_at=now()`, started, overview.LastScannedBlock, JSON(overview))
	return err
}

func (s *Store) LoadRewardOverview(ctx context.Context) (model.RewardOverview, error) {
	var raw []byte
	var overview model.RewardOverview
	err := s.DB.QueryRowContext(ctx, `SELECT payload FROM reward_monitor_state WHERE id=1`).Scan(&raw)
	if err != nil {
		return overview, err
	}
	err = json.Unmarshal(raw, &overview)
	return overview, err
}

func (s *Store) SaveRewardObservation(ctx context.Context, x model.RewardObservation) error {
	if x.AuthoredAt.IsZero() {
		x.AuthoredAt = time.Now().UTC()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO reward_events(block_number,block_hash,authored_at,expected_planck,credited_planck,wallet_before_planck,wallet_after_planck,pot_before_planck,verification,source_count,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(block_number) DO UPDATE SET source_count=GREATEST(reward_events.source_count,excluded.source_count),verification=CASE WHEN excluded.source_count>=reward_events.source_count THEN excluded.verification ELSE reward_events.verification END,evidence=reward_events.evidence||excluded.evidence`, x.BlockNumber, x.BlockHash, x.AuthoredAt, x.ExpectedPlanck, x.CreditedPlanck, x.WalletBeforePlanck, x.WalletAfterPlanck, x.PotBeforePlanck, x.Verification, x.SourceCount, JSON(x.Evidence))
	return err
}

func (s *Store) RewardPage(ctx context.Context, limit int, before int64) ([]model.RewardObservation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT block_number,block_hash,authored_at,expected_planck::text,credited_planck::text,wallet_before_planck::text,wallet_after_planck::text,pot_before_planck::text,verification,source_count,evidence FROM reward_events WHERE block_number<$1 ORDER BY block_number DESC LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RewardObservation{}
	for rows.Next() {
		var x model.RewardObservation
		var evidence []byte
		if err := rows.Scan(&x.BlockNumber, &x.BlockHash, &x.AuthoredAt, &x.ExpectedPlanck, &x.CreditedPlanck, &x.WalletBeforePlanck, &x.WalletAfterPlanck, &x.PotBeforePlanck, &x.Verification, &x.SourceCount, &evidence); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &x.Evidence)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) RewardStats(ctx context.Context, overview *model.RewardOverview) error {
	var lastAt sql.NullTime
	var lastAmount sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT max(authored_at),COALESCE((SELECT credited_planck::text FROM reward_events ORDER BY block_number DESC LIMIT 1),'0'),count(*),COALESCE(sum(credited_planck),0)::text FROM reward_events`).Scan(&lastAt, &lastAmount, &overview.RewardTotalCount, &overview.RewardTotalPlanck); err != nil {
		return err
	}
	if lastAt.Valid {
		overview.LastRewardAt = lastAt.Time.UTC()
	}
	if lastAmount.Valid {
		overview.LastRewardPlanck = lastAmount.String
	} else {
		overview.LastRewardPlanck = "0"
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(credited_planck),0)::text FROM reward_events WHERE authored_at>=now()-interval '24 hours'`).Scan(&overview.Reward24hCount, &overview.Reward24hPlanck); err != nil {
		return err
	}
	if overview.RewardTotalPlanck == "" {
		overview.RewardTotalPlanck = "0"
	}
	if overview.Reward24hPlanck == "" {
		overview.Reward24hPlanck = "0"
	}
	return nil
}

func (s *Store) RewardDaily(ctx context.Context, days int) ([]model.RewardDaily, error) {
	if days <= 0 || days > 90 {
		days = 30
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT to_char((authored_at AT TIME ZONE 'Asia/Tokyo')::date,'YYYY-MM-DD'),sum(credited_planck)::text,count(*) FROM reward_events WHERE authored_at>=now()-make_interval(days => $1) GROUP BY 1 ORDER BY 1`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RewardDaily{}
	for rows.Next() {
		var x model.RewardDaily
		if err := rows.Scan(&x.Day, &x.AmountPlanck, &x.Count); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
