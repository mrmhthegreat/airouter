package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// SQLStore is the SQLite-backed implementation of Store. It works with either
// github.com/mattn/go-sqlite3 (CGO) or modernc.org/sqlite (pure Go) — open the
// *sql.DB yourself with the driver you want and pass it to NewSQLStore.
type SQLStore struct{ db *sql.DB }

// Schema is the full set of tables airouter needs. It is applied idempotently
// by NewSQLStore (CREATE TABLE IF NOT EXISTS).
const schema = `
CREATE TABLE IF NOT EXISTS tenants(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, api_key_hash TEXT NOT NULL UNIQUE,
  tz TEXT NOT NULL DEFAULT 'UTC', daily_request_limit INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS providers(
  id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, tenant_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL, base_url TEXT NOT NULL, api_key_env TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100,
  rpm INTEGER NOT NULL DEFAULT 0, rpd INTEGER NOT NULL DEFAULT 0, tpm INTEGER NOT NULL DEFAULT 0,
  ash INTEGER NOT NULL DEFAULT 0, asd INTEGER NOT NULL DEFAULT 0,
  workers INTEGER NOT NULL DEFAULT 2, reset_tz TEXT NOT NULL DEFAULT 'UTC',
  json_mode INTEGER, system_role INTEGER, disabled INTEGER NOT NULL DEFAULT 0,
  category TEXT NOT NULL DEFAULT 'text',
  UNIQUE(tenant_id, name));
CREATE TABLE IF NOT EXISTS usage(
  tenant_id TEXT NOT NULL, provider_id INTEGER NOT NULL, day TEXT NOT NULL,
  requests INTEGER NOT NULL DEFAULT 0, tokens INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(tenant_id, provider_id, day));
CREATE INDEX IF NOT EXISTS usage_provider_day ON usage(provider_id, day);
CREATE TABLE IF NOT EXISTS provider_pause(provider_id INTEGER PRIMARY KEY, until TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '');`

// NewSQLStore runs the schema migration and returns a ready store. It also pins
// the connection pool to a single open connection, which is the right setting
// for SQLite (avoids "database is locked" errors under concurrent writers).
//
// For databases created before the `category`, `ash`, or `asd` columns existed,
// ALTER TABLE migrations add them with safe defaults so existing providers
// continue to work.
func NewSQLStore(db *sql.DB) (*SQLStore, error) {
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrateColumns(db, "providers", map[string]string{
		"category": "TEXT NOT NULL DEFAULT 'text'",
		"ash":      "INTEGER NOT NULL DEFAULT 0",
		"asd":      "INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return nil, fmt.Errorf("migrate columns: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// migrateColumns adds any missing columns to a table. Each map entry is
// column-name → SQLite column definition (type + constraints). SQLite's
// ALTER TABLE ADD COLUMN supports NOT NULL defaults as long as the default
// is non-null, which is why all of our defaults are explicit.
func migrateColumns(db *sql.DB, table string, cols map[string]string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	rows.Close()
	for col, def := range cols {
		if existing[col] {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def)); err != nil {
			return fmt.Errorf("add column %s: %w", col, err)
		}
	}
	return nil
}

func (s *SQLStore) Close() error { return s.db.Close() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ptrToNull(b *bool) any {
	if b == nil {
		return nil
	}
	return b2i(*b)
}

func nullToPtr(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 != 0
	return &v
}

func (s *SQLStore) CreateTenant(ctx context.Context, t types.Tenant, keyHash string) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenants(id,name,api_key_hash,tz,daily_request_limit,disabled,created_at) VALUES(?,?,?,?,?,?,?)`,
		t.ID, t.Name, keyHash, t.TZ, t.DailyRequestLimit, b2i(t.Disabled), t.CreatedAt.Format(time.RFC3339))
	return err
}

func (s *SQLStore) UpdateTenantKeyHash(ctx context.Context, id, keyHash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tenants SET api_key_hash=? WHERE id=?`, keyHash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return types.ErrNotFound
	}
	return nil
}

func scanTenant(row interface{ Scan(...any) error }) (*types.Tenant, error) {
	var t types.Tenant
	var dis int
	var created string
	if err := row.Scan(&t.ID, &t.Name, &t.TZ, &t.DailyRequestLimit, &dis, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	t.Disabled = dis != 0
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &t, nil
}

const tenantCols = `id,name,tz,daily_request_limit,disabled,created_at`

func (s *SQLStore) TenantByID(ctx context.Context, id string) (*types.Tenant, error) {
	return scanTenant(s.db.QueryRowContext(ctx, `SELECT `+tenantCols+` FROM tenants WHERE id=?`, id))
}

func (s *SQLStore) TenantByKeyHash(ctx context.Context, h string) (*types.Tenant, error) {
	return scanTenant(s.db.QueryRowContext(ctx, `SELECT `+tenantCols+` FROM tenants WHERE api_key_hash=?`, h))
}

func (s *SQLStore) ListTenants(ctx context.Context) ([]types.Tenant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tenantCols+` FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *SQLStore) SaveProvider(ctx context.Context, p *types.ProviderConfig) error {
	if p.Category == "" {
		p.Category = "text"
	}
	args := []any{p.Name, p.TenantID, p.Kind, p.BaseURL, p.APIKeyEnv, p.APIKey, p.Model, p.Priority,
		p.RPM, p.RPD, p.TPM, p.ASH, p.ASD, p.Workers, p.ResetTZ, ptrToNull(p.JSONMode), ptrToNull(p.SystemRole), b2i(p.Disabled), p.Category}
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO providers(name,tenant_id,kind,base_url,api_key_env,api_key,model,priority,rpm,rpd,tpm,ash,asd,workers,reset_tz,json_mode,system_role,disabled,category)
                        VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
		if err != nil {
			return err
		}
		p.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE providers SET name=?,tenant_id=?,kind=?,base_url=?,api_key_env=?,api_key=?,model=?,priority=?,
                rpm=?,rpd=?,tpm=?,ash=?,asd=?,workers=?,reset_tz=?,json_mode=?,system_role=?,disabled=?,category=? WHERE id=?`, append(args, p.ID)...)
	return err
}

func (s *SQLStore) ListProviders(ctx context.Context) ([]types.ProviderConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,name,tenant_id,kind,base_url,api_key_env,api_key,model,priority,rpm,rpd,tpm,ash,asd,workers,reset_tz,json_mode,system_role,disabled,category FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.ProviderConfig
	for rows.Next() {
		var p types.ProviderConfig
		var jm, sr sql.NullInt64
		var dis int
		var cat sql.NullString
		if err := rows.Scan(&p.ID, &p.Name, &p.TenantID, &p.Kind, &p.BaseURL, &p.APIKeyEnv, &p.APIKey, &p.Model, &p.Priority,
			&p.RPM, &p.RPD, &p.TPM, &p.ASH, &p.ASD, &p.Workers, &p.ResetTZ, &jm, &sr, &dis, &cat); err != nil {
			return nil, err
		}
		p.JSONMode, p.SystemRole, p.Disabled = nullToPtr(jm), nullToPtr(sr), dis != 0
		if cat.Valid && cat.String != "" {
			p.Category = cat.String
		} else {
			p.Category = "text"
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLStore) DeleteProvider(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id=?`, id)
	return err
}

func (s *SQLStore) SetPause(ctx context.Context, pid int64, until time.Time, reason string) error {
	if until.IsZero() {
		_, err := s.db.ExecContext(ctx, `DELETE FROM provider_pause WHERE provider_id=?`, pid)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO provider_pause(provider_id,until,reason) VALUES(?,?,?)
                ON CONFLICT(provider_id) DO UPDATE SET until=excluded.until, reason=excluded.reason`,
		pid, until.UTC().Format(time.RFC3339), reason)
	return err
}

func (s *SQLStore) GetPause(ctx context.Context, pid int64) (time.Time, string, error) {
	var u, reason string
	err := s.db.QueryRowContext(ctx, `SELECT until, reason FROM provider_pause WHERE provider_id=?`, pid).Scan(&u, &reason)
	if err == sql.ErrNoRows {
		return time.Time{}, "", nil
	}
	if err != nil {
		return time.Time{}, "", err
	}
	t, _ := time.Parse(time.RFC3339, u)
	return t, reason, nil
}

func (s *SQLStore) AddUsage(ctx context.Context, tenant string, pid int64, day string, req, tok, errs int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage(tenant_id,provider_id,day,requests,tokens,errors) VALUES(?,?,?,?,?,?)
                ON CONFLICT(tenant_id,provider_id,day) DO UPDATE SET requests=requests+excluded.requests, tokens=tokens+excluded.tokens, errors=errors+excluded.errors`,
		tenant, pid, day, req, tok, errs)
	return err
}

func (s *SQLStore) ProviderDayUsage(ctx context.Context, pid int64, day string) (int, int, error) {
	var r, t int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(requests),0), COALESCE(SUM(tokens),0) FROM usage WHERE provider_id=? AND day=?`, pid, day).Scan(&r, &t)
	return r, t, err
}

func (s *SQLStore) TenantProviderUsage(ctx context.Context, tenant string, pid int64, day string) (int, int, int, error) {
	var r, t, e int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(requests),0), COALESCE(SUM(tokens),0), COALESCE(SUM(errors),0) FROM usage WHERE tenant_id=? AND provider_id=? AND day=?`,
		tenant, pid, day).Scan(&r, &t, &e)
	return r, t, e, err
}

func (s *SQLStore) TenantDayUsage(ctx context.Context, tenant, day string) (int, error) {
	var r int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(requests),0) FROM usage WHERE tenant_id=? AND day=?`, tenant, day).Scan(&r)
	return r, err
}
