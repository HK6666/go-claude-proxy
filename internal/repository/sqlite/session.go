package sqlite

import (
	"database/sql"
	"time"

	"github.com/Bowl42/maxx-next/internal/domain"
)

type SessionRepository struct {
	db *DB
}

func NewSessionRepository(db *DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(s *domain.Session) error {
	now := time.Now()
	s.CreatedAt = now
	s.UpdatedAt = now

	result, err := r.db.db.Exec(
		`INSERT INTO sessions (created_at, updated_at, session_id, client_type, project_id) VALUES (?, ?, ?, ?, ?)`,
		s.CreatedAt, s.UpdatedAt, s.SessionID, s.ClientType, s.ProjectID,
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	s.ID = uint64(id)
	return nil
}

func (r *SessionRepository) Update(s *domain.Session) error {
	s.UpdatedAt = time.Now()
	_, err := r.db.db.Exec(
		`UPDATE sessions SET updated_at = ?, client_type = ?, project_id = ? WHERE id = ?`,
		s.UpdatedAt, s.ClientType, s.ProjectID, s.ID,
	)
	return err
}

func (r *SessionRepository) GetBySessionID(sessionID string) (*domain.Session, error) {
	row := r.db.db.QueryRow(`SELECT id, created_at, updated_at, session_id, client_type, project_id FROM sessions WHERE session_id = ?`, sessionID)
	var s domain.Session
	err := row.Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt, &s.SessionID, &s.ClientType, &s.ProjectID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *SessionRepository) List() ([]*domain.Session, error) {
	rows, err := r.db.db.Query(`SELECT id, created_at, updated_at, session_id, client_type, project_id FROM sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*domain.Session
	for rows.Next() {
		var s domain.Session
		err := rows.Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt, &s.SessionID, &s.ClientType, &s.ProjectID)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, &s)
	}
	return sessions, rows.Err()
}
