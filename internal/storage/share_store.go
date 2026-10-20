package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// ShareStore persists share metadata. It is a view over the shared database handle,
// not an owner: the bundle closes the handle, so this store has no Close of its own.
type ShareStore struct {
	db *sql.DB
}

func NewShareStore(db *sql.DB) *ShareStore {
	return &ShareStore{db: db}
}

func (s *ShareStore) Save(ctx context.Context, share *domain.ShareRecord) error {
	modsJSON, err := json.Marshal(share.Mods)
	if err != nil {
		return fmt.Errorf("marshal mods: %w", err)
	}

	linksJSON, err := json.Marshal(share.Links)
	if err != nil {
		return fmt.Errorf("marshal links: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO shares (id, game_id, mods, links, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, share.ID, share.GameID, modsJSON, linksJSON,
		formatTime(share.CreatedAt), formatTime(share.UpdatedAt))

	return err
}

func (s *ShareStore) Get(ctx context.Context, id string) (*domain.ShareRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, game_id, mods, links, created_at, updated_at FROM shares WHERE id = ?
	`, id)

	share, err := s.scanShare(row)
	if err != nil {
		return nil, notFound(err, domain.ErrShareNotFound)
	}

	return share, nil
}

func (s *ShareStore) List(ctx context.Context) ([]domain.ShareRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, game_id, mods, links, created_at, updated_at FROM shares ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shares []domain.ShareRecord

	for rows.Next() {
		share, err := s.scanShare(rows)
		if err != nil {
			return nil, err
		}

		shares = append(shares, *share)
	}

	return shares, rows.Err()
}

func (s *ShareStore) ListByGame(ctx context.Context, gameID string) ([]domain.ShareRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, game_id, mods, links, created_at, updated_at FROM shares WHERE game_id = ? ORDER BY created_at DESC
	`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shares []domain.ShareRecord

	for rows.Next() {
		share, err := s.scanShare(rows)
		if err != nil {
			return nil, err
		}

		shares = append(shares, *share)
	}

	return shares, rows.Err()
}

func (s *ShareStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE id = ?`, id)
	return err
}

func (s *ShareStore) GetByLink(
	ctx context.Context,
	link domain.ShareLink,
) (*domain.ShareRecord, error) {
	shares, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	for i := range shares {
		for _, l := range shares[i].Links {
			if l.URL == link.URL {
				return &shares[i], nil
			}
		}
	}

	return nil, domain.ErrShareNotFound
}

type shareScanner interface {
	Scan(dest ...any) error
}

// notFound turns a missing row into the typed error. A raw sql.ErrNoRows escaping
// the store would force every caller to match on a database package's sentinel
// instead of the domain's.
func notFound(err, notFoundErr error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFoundErr
	}

	return err
}

func (s *ShareStore) scanShare(row shareScanner) (*domain.ShareRecord, error) {
	var (
		id, gameID   string
		modsJSON     []byte
		linksJSON    []byte
		createdAtStr string
		updatedAtStr string
	)

	err := row.Scan(&id, &gameID, &modsJSON, &linksJSON, &createdAtStr, &updatedAtStr)
	if err != nil {
		return nil, err
	}

	var mods []domain.Mod
	if err := json.Unmarshal(modsJSON, &mods); err != nil {
		return nil, fmt.Errorf("unmarshal mods: %w", err)
	}

	var links []domain.ShareLink
	if err := json.Unmarshal(linksJSON, &links); err != nil {
		return nil, fmt.Errorf("unmarshal links: %w", err)
	}

	// parseTime never reports an error: an unreadable timestamp yields the zero time
	// an unreadable timestamp yields the zero time: not failing the whole read
	// is still a share the user can look at and revoke.
	createdAt := parseTime(createdAtStr)
	updatedAt := parseTime(updatedAtStr)

	return &domain.ShareRecord{
		ID:        id,
		GameID:    gameID,
		Mods:      mods,
		Links:     links,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}
