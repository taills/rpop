package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("site not found")

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func Migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sites (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, config_json BLOB NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, auto_start INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS site_secrets (
		site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		name TEXT NOT NULL, value BLOB NOT NULL, PRIMARY KEY(site_id,name)
	);`)
	if err != nil {
		return err
	}

	// Upgrade databases created before auto_start was introduced.
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(sites)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "auto_start" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !found {
		_, err = db.ExecContext(ctx, `ALTER TABLE sites ADD COLUMN auto_start INTEGER NOT NULL DEFAULT 0`)
	}
	return err
}

func (s *Store) List(ctx context.Context) ([]Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,config_json,auto_start,updated_at FROM sites ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Site{}
	for rows.Next() {
		var x Site
		var raw []byte
		var autoStart int
		if err := rows.Scan(&x.ID, &x.Name, &raw, &autoStart, &x.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &x.Config); err != nil {
			return nil, err
		}
		x.AutoStart = autoStart != 0
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Site, error) {
	var x Site
	var raw []byte
	var autoStart int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,config_json,auto_start,updated_at FROM sites WHERE id=?`, id).
		Scan(&x.ID, &x.Name, &raw, &autoStart, &x.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	if err := json.Unmarshal(raw, &x.Config); err != nil {
		return x, err
	}
	x.AutoStart = autoStart != 0
	return x, nil
}

func (s *Store) SaveMany(ctx context.Context, sites []Site) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, x := range sites {
		if err := saveSite(ctx, tx, x); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Save(ctx context.Context, x Site) error {
	return saveSite(ctx, s.db, x)
}

type siteExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func saveSite(ctx context.Context, db siteExecer, x Site) error {
	raw, err := json.Marshal(x.Config)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `INSERT INTO sites(id,name,config_json,auto_start,created_at,updated_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
		name=excluded.name,config_json=excluded.config_json,auto_start=excluded.auto_start,updated_at=excluded.updated_at`,
		x.ID, x.Name, raw, x.AutoStart, now, now)
	return err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SaveSecret(ctx context.Context, siteID, name string, value []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_secrets(site_id,name,value) VALUES(?,?,?)
		ON CONFLICT(site_id,name) DO UPDATE SET value=excluded.value`, siteID, name, value)
	return err
}

func (s *Store) Secret(ctx context.Context, siteID, name string) ([]byte, error) {
	var value []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM site_secrets WHERE site_id=? AND name=?`, siteID, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return value, err
}

func (s *Store) AutoStartSites(ctx context.Context) ([]Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,config_json,auto_start,updated_at FROM sites WHERE auto_start=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		var x Site
		var raw []byte
		var autoStart int
		if err := rows.Scan(&x.ID, &x.Name, &raw, &autoStart, &x.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &x.Config); err != nil {
			return nil, err
		}
		x.AutoStart = autoStart != 0
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) Check(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite ping: %w", err)
	}
	return nil
}
