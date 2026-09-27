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
}

const nodeColumns = `id,name,relay_address,token_hash,token_expires_at,cert_generation,cert_not_after,created_at,registered_at`

type rowScanner interface{ Scan(...any) error }

func scanNode(row rowScanner) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.Name, &n.RelayAddress, &n.TokenHash, &n.TokenExpiresAt, &n.CertGeneration, &n.CertNotAfter, &n.CreatedAt, &n.RegisteredAt)
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

// SaveNode inserts or replaces a node; CreatedAt is set on first insert.
func (s *Store) SaveNode(ctx context.Context, n Node) error {
	if n.CreatedAt == "" {
		n.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO nodes(`+nodeColumns+`) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,relay_address=excluded.relay_address,token_hash=excluded.token_hash,
		token_expires_at=excluded.token_expires_at,cert_generation=excluded.cert_generation,cert_not_after=excluded.cert_not_after,
		registered_at=excluded.registered_at`,
		n.ID, n.Name, n.RelayAddress, n.TokenHash, n.TokenExpiresAt, n.CertGeneration, n.CertNotAfter, n.CreatedAt, n.RegisteredAt)
	return err
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
