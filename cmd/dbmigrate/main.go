package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shiden-guardian/shiden-guardian/internal/store"
)

var tables = []string{"users", "sessions", "runtime_state", "incidents", "diagnoses", "remediation_actions", "settings", "audit_events", "jobs", "reward_events", "reward_monitor_state"}

func main() {
	mode := flag.String("mode", "dry-run", "dry-run, apply, verify, or rotate-admin")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	switch *mode {
	case "dry-run", "apply":
		err = apply(ctx, *mode == "dry-run")
	case "verify":
		err = verify(ctx)
	case "rotate-admin":
		err = rotateAdmin(ctx)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		slog.Error("database migration", "mode", *mode, "error", err)
		os.Exit(1)
	}
	slog.Info("database migration succeeded", "mode", *mode)
}

func secret(name string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(name + "_FILE")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		if value := strings.TrimSpace(string(data)); value != "" {
			return value, nil
		}
	}
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}
	return "", fmt.Errorf("%s is required", name)
}

func passwordFromURL(name string) (string, error) {
	value, err := secret(name)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return "", fmt.Errorf("%s is not a database URL", name)
	}
	password, ok := parsed.User.Password()
	if !ok || password == "" {
		return "", fmt.Errorf("%s does not contain a password", name)
	}
	return password, nil
}

func quoteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func apply(ctx context.Context, dryRun bool) error {
	adminURL, err := secret("MIGRATION_DATABASE_URL")
	if err != nil {
		return err
	}
	passwords := map[string]string{}
	for role, name := range map[string]string{"guardian_api": "API_DATABASE_URL", "guardian_auth": "AUTH_DATABASE_URL", "guardian_controller": "CONTROLLER_DATABASE_URL"} {
		passwords[role], err = passwordFromURL(name)
		if err != nil {
			return err
		}
	}
	passwords["guardian_backup"], err = secret("BACKUP_PASSWORD")
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, adminURL)
	if err != nil {
		return err
	}
	defer db.DB.Close()
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(834733101)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL password_encryption = 'scram-sha-256'`); err != nil {
		return err
	}
	for _, role := range []string{"guardian_owner", "guardian_api", "guardian_auth", "guardian_controller", "guardian_backup"} {
		login := "LOGIN"
		if role == "guardian_owner" {
			login = "NOLOGIN"
		}
		statement := fmt.Sprintf(`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=%s) THEN CREATE ROLE %s; END IF; END $$; ALTER ROLE %s %s NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION`, quoteLiteral(role), role, role, login)
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create role %s: %w", role, err)
		}
	}
	for role, password := range passwords {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER ROLE %s PASSWORD %s", role, quoteLiteral(password))); err != nil {
			return fmt.Errorf("set role password %s: %w", role, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `ALTER ROLE guardian_backup SET default_transaction_read_only = on`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `REVOKE ALL ON SCHEMA public FROM PUBLIC; REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC; ALTER SCHEMA public OWNER TO guardian_owner`); err != nil {
		return err
	}
	for _, table := range tables {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE IF EXISTS public.%s OWNER TO guardian_owner", table)); err != nil {
			return fmt.Errorf("existing owner %s: %w", table, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL ROLE guardian_owner`); err != nil {
		return err
	}
	for _, statement := range store.MigrationStatements() {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("schema migration: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `RESET ROLE`); err != nil {
		return err
	}
	for _, table := range tables {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE public.%s OWNER TO guardian_owner", table)); err != nil {
			return fmt.Errorf("owner %s: %w", table, err)
		}
	}
	roles := "guardian_api,guardian_auth,guardian_controller,guardian_backup"
	if _, err = tx.ExecContext(ctx, "REVOKE ALL ON ALL TABLES IN SCHEMA public FROM "+roles+"; GRANT USAGE ON SCHEMA public TO "+roles); err != nil {
		return err
	}
	grants := []string{
		`GRANT SELECT (id,username,active) ON users TO guardian_api`,
		`GRANT SELECT (token_hash,user_id,csrf_hash,expires_at) ON sessions TO guardian_api`,
		`GRANT SELECT ON runtime_state,incidents,diagnoses,settings,reward_events,reward_monitor_state TO guardian_api`,
		`GRANT SELECT,INSERT ON audit_events TO guardian_api`,
		`GRANT INSERT ON jobs TO guardian_api`,
		`GRANT SELECT,INSERT,UPDATE,DELETE ON users,sessions TO guardian_auth`,
		`GRANT SELECT,INSERT,UPDATE ON settings TO guardian_auth`,
		`GRANT INSERT ON audit_events TO guardian_auth`,
		`GRANT SELECT,INSERT,UPDATE,DELETE ON runtime_state,incidents,diagnoses,remediation_actions,settings,audit_events,jobs,reward_events,reward_monitor_state TO guardian_controller`,
		`GRANT SELECT ON users,sessions,runtime_state,incidents,diagnoses,remediation_actions,settings,audit_events,jobs,reward_events,reward_monitor_state TO guardian_backup`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE guardian_owner IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC`,
	}
	for _, statement := range grants {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("grant: %w", err)
		}
	}
	if dryRun {
		return tx.Rollback()
	}
	return tx.Commit()
}

type permissionCheck struct {
	role, sql string
	want      bool
}

func verify(ctx context.Context) error {
	checks := map[string][]permissionCheck{
		"API_DATABASE_URL": {
			{"guardian_api", `SELECT has_schema_privilege(current_user,'public','CREATE')`, false},
			{"guardian_api", `SELECT has_column_privilege(current_user,'users','username','SELECT')`, true},
			{"guardian_api", `SELECT has_column_privilege(current_user,'users','password_hash','SELECT')`, false},
			{"guardian_api", `SELECT has_table_privilege(current_user,'settings','UPDATE')`, false},
			{"guardian_api", `SELECT has_table_privilege(current_user,'remediation_actions','INSERT')`, false},
			{"guardian_api", `SELECT has_table_privilege(current_user,'jobs','INSERT')`, true},
		},
		"AUTH_DATABASE_URL": {
			{"guardian_auth", `SELECT has_schema_privilege(current_user,'public','CREATE')`, false},
			{"guardian_auth", `SELECT has_table_privilege(current_user,'users','SELECT')`, true},
			{"guardian_auth", `SELECT has_table_privilege(current_user,'sessions','DELETE')`, true},
			{"guardian_auth", `SELECT has_table_privilege(current_user,'remediation_actions','INSERT')`, false},
		},
		"CONTROLLER_DATABASE_URL": {
			{"guardian_controller", `SELECT has_schema_privilege(current_user,'public','CREATE')`, false},
			{"guardian_controller", `SELECT has_table_privilege(current_user,'remediation_actions','INSERT')`, true},
			{"guardian_controller", `SELECT has_table_privilege(current_user,'users','SELECT')`, false},
			{"guardian_controller", `SELECT has_table_privilege(current_user,'sessions','DELETE')`, false},
		},
		"BACKUP_DATABASE_URL": {
			{"guardian_backup", `SELECT has_schema_privilege(current_user,'public','CREATE')`, false},
			{"guardian_backup", `SELECT has_table_privilege(current_user,'users','SELECT')`, true},
			{"guardian_backup", `SELECT has_table_privilege(current_user,'settings','UPDATE')`, false},
		},
	}
	for name, roleChecks := range checks {
		var value string
		var err error
		if name == "BACKUP_DATABASE_URL" {
			var password string
			password, err = secret("BACKUP_PASSWORD")
			if err == nil {
				value = (&url.URL{Scheme: "postgres", User: url.UserPassword("guardian_backup", password), Host: "postgres:5432", Path: "/guardian", RawQuery: "sslmode=disable"}).String()
			}
		} else {
			value, err = secret(name)
		}
		if err != nil {
			return err
		}
		db, err := store.Open(ctx, value)
		if err != nil {
			return fmt.Errorf("connect %s: %w", name, err)
		}
		var current string
		if err = db.DB.QueryRowContext(ctx, `SELECT current_user`).Scan(&current); err != nil {
			db.DB.Close()
			return err
		}
		if current != roleChecks[0].role {
			db.DB.Close()
			return fmt.Errorf("%s connected as %s", name, current)
		}
		for _, check := range roleChecks {
			var got bool
			if err = db.DB.QueryRowContext(ctx, check.sql).Scan(&got); err != nil {
				db.DB.Close()
				return err
			}
			if got != check.want {
				db.DB.Close()
				return fmt.Errorf("%s permission mismatch for %s", check.role, check.sql)
			}
		}
		if err = verifyRoleTransactions(ctx, db.DB, current); err != nil {
			db.DB.Close()
			return fmt.Errorf("%s transaction verification: %w", current, err)
		}
		db.DB.Close()
	}
	return nil
}

func verifyRoleTransactions(ctx context.Context, db *sql.DB, role string) error {
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	allowed := map[string][]string{
		"guardian_api": {
			`SELECT id,username,active FROM users LIMIT 0`,
			`SELECT token_hash,user_id,csrf_hash,expires_at FROM sessions LIMIT 0`,
			`SELECT * FROM runtime_state,incidents,diagnoses,settings,reward_events,reward_monitor_state LIMIT 0`,
			fmt.Sprintf(`INSERT INTO jobs(id,kind,payload) VALUES('verify_api_job_%s','diagnose_now','{}')`, suffix),
			fmt.Sprintf(`INSERT INTO audit_events(id,action,actor,result,details) VALUES('verify_api_audit_%s','verify','dbmigrate','success','{}')`, suffix),
		},
		"guardian_auth": {
			`SELECT * FROM users,sessions LIMIT 0`,
			fmt.Sprintf(`INSERT INTO users(id,username,password_hash,totp_secret,recovery_hashes,active) VALUES('verify_auth_user_%s','verify_auth_%s','x','x','[]',false)`, suffix, suffix),
			fmt.Sprintf(`INSERT INTO sessions(token_hash,user_id,csrf_hash,expires_at) VALUES('verify_auth_session_%s','verify_auth_user_%s','x',now()+interval '1 minute')`, suffix, suffix),
			fmt.Sprintf(`INSERT INTO settings(key,value) VALUES('verify_auth_setting_%s','false')`, suffix),
			fmt.Sprintf(`INSERT INTO audit_events(id,action,actor,result,details) VALUES('verify_auth_audit_%s','verify','dbmigrate','success','{}')`, suffix),
		},
		"guardian_controller": {
			`SELECT * FROM runtime_state,incidents,diagnoses,remediation_actions,settings,audit_events,jobs,reward_events,reward_monitor_state LIMIT 0`,
			`SELECT last_scanned_block FROM reward_monitor_state WHERE false FOR UPDATE`,
			`UPDATE reward_monitor_state SET updated_at=updated_at WHERE false`,
			fmt.Sprintf(`INSERT INTO remediation_actions(id,idempotency_key,requested_by,reason,mode,status) VALUES('verify_controller_action_%s','verify_controller_%s','dbmigrate','permission verification','manual','pending')`, suffix, suffix),
			fmt.Sprintf(`INSERT INTO jobs(id,kind,payload) VALUES('verify_controller_job_%s','verify','{}')`, suffix),
			fmt.Sprintf(`INSERT INTO settings(key,value) VALUES('verify_controller_setting_%s','false')`, suffix),
			fmt.Sprintf(`INSERT INTO audit_events(id,action,actor,result,details) VALUES('verify_controller_audit_%s','verify','dbmigrate','success','{}')`, suffix),
		},
		"guardian_backup": {
			`SELECT * FROM users,sessions,runtime_state,incidents,diagnoses,remediation_actions,settings,audit_events,jobs,reward_events,reward_monitor_state LIMIT 0`,
		},
	}
	denied := map[string][]string{
		"guardian_api": {
			`SELECT password_hash,totp_secret,recovery_hashes FROM users LIMIT 0`,
			`UPDATE settings SET value='false' WHERE false`,
			fmt.Sprintf(`INSERT INTO remediation_actions(id,idempotency_key,requested_by,reason,mode,status) VALUES('denied_api_action_%s','denied_api_%s','dbmigrate','permission verification','manual','pending')`, suffix, suffix),
		},
		"guardian_auth": {
			fmt.Sprintf(`INSERT INTO remediation_actions(id,idempotency_key,requested_by,reason,mode,status) VALUES('denied_auth_action_%s','denied_auth_%s','dbmigrate','permission verification','manual','pending')`, suffix, suffix),
		},
		"guardian_controller": {
			`SELECT * FROM users LIMIT 0`,
			`SELECT * FROM sessions LIMIT 0`,
		},
		"guardian_backup": {
			fmt.Sprintf(`INSERT INTO audit_events(id,action,actor,result,details) VALUES('denied_backup_audit_%s','verify','dbmigrate','denied','{}')`, suffix),
		},
	}
	if err := runAllowed(ctx, db, allowed[role]); err != nil {
		return err
	}
	for _, statement := range denied[role] {
		if err := requireDenied(ctx, db, statement); err != nil {
			return err
		}
	}
	return nil
}

func runAllowed(ctx context.Context, db *sql.DB, statements []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("allowed statement failed: %w", err)
		}
	}
	return tx.Rollback()
}

func requireDenied(ctx context.Context, db *sql.DB, statement string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, statement)
	if err == nil {
		return errors.New("forbidden statement succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" && pgErr.Code != "25006" {
		return fmt.Errorf("forbidden statement failed for an unexpected reason: %w", err)
	}
	return nil
}

func rotateAdmin(ctx context.Context) error {
	adminURL, err := secret("MIGRATION_DATABASE_URL")
	if err != nil {
		return err
	}
	password, err := secret("POSTGRES_NEW_PASSWORD")
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, adminURL)
	if err != nil {
		return err
	}
	defer db.DB.Close()
	tx, err := db.DB.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(834733101)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL password_encryption = 'scram-sha-256'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "ALTER ROLE guardian PASSWORD "+quoteLiteral(password)); err != nil {
		return err
	}
	return tx.Commit()
}
