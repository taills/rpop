package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Node is a registered or pre-provisioned data-plane node.
type Node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// RelayAddress is the host:port other nodes dial to reach this node's relay port.
	RelayAddress string `json:"relayAddress,omitempty"`
	// TokenHash is the SHA-256 of the outstanding single-use join token, if any.
	TokenHash      string `json:"-"`
	TokenExpiresAt string `json:"tokenExpiresAt,omitempty"`
	// CertGeneration counts registrations. Certificates carry the generation they were issued for, and only the
	// current one authenticates: registering again revokes older certificates, while renewals keep the
	// generation, so a lost renewal response never locks the node out.
	CertGeneration int64  `json:"-"`
	CertNotAfter   string `json:"certNotAfter,omitempty"`
	CreatedAt      string `json:"createdAt"`
	RegisteredAt   string `json:"registeredAt,omitempty"`
	// LogHWM is the highest log segment number the controller has durably ingested and persisted for this node
	// (D24); it is populated on read so log ingestion can check idempotency without a second query, but
	// SaveNode never writes it back (see UpdateNodeLogHWM), so it cannot be clobbered by an unrelated node edit.
	LogHWM uint64 `json:"-"`
}

const nodeColumns = `id,name,relay_address,token_hash,token_expires_at,cert_generation,cert_not_after,created_at,registered_at,log_hwm`

type rowScanner interface{ Scan(...any) error }

func scanNode(row rowScanner) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.Name, &n.RelayAddress, &n.TokenHash, &n.TokenExpiresAt, &n.CertGeneration, &n.CertNotAfter, &n.CreatedAt, &n.RegisteredAt, &n.LogHWM)
	return n, err
}

// ListNodes returns every node ordered by ID.
func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeColumns+` FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// GetNode returns one node.
func (s *Store) GetNode(ctx context.Context, id string) (Node, error) {
	n, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNodeNotFound
	}
	return n, err
}

// SaveNode inserts or replaces a node; CreatedAt is set on first insert. It never writes LogHWM on an existing
// row (see UpdateNodeLogHWM): callers construct a Node from GetNode/ListNodes and only mean to change the
// fields they set, and log ingestion must be free to advance the high-water mark without losing a concurrent,
// unrelated edit (or vice versa).
func (s *Store) SaveNode(ctx context.Context, n Node) error {
	if n.CreatedAt == "" {
		n.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO nodes(`+nodeColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,relay_address=excluded.relay_address,token_hash=excluded.token_hash,
		token_expires_at=excluded.token_expires_at,cert_generation=excluded.cert_generation,cert_not_after=excluded.cert_not_after,
		registered_at=excluded.registered_at`,
		n.ID, n.Name, n.RelayAddress, n.TokenHash, n.TokenExpiresAt, n.CertGeneration, n.CertNotAfter, n.CreatedAt, n.RegisteredAt, n.LogHWM)
	return err
}

// UpdateNodeLogHWM persists the highest log segment number the controller has durably ingested from a node
// (D24): the log ingest handler calls this only after every record in the segment has been written to its
// destination, so a controller restart between the write and this call simply replays that segment (the node
// resends anything at or below its own idea of the acked segment, which is harmless to write again), never
// skips one. It is the sole writer of this column, so it cannot race with SaveNode (see its doc comment).
func (s *Store) UpdateNodeLogHWM(ctx context.Context, id string, hwm uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE nodes SET log_hwm=? WHERE id=?`, hwm, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// DeleteNode removes a node.
func (s *Store) DeleteNode(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNodeNotFound
	}
	return nil
}
