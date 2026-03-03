package sqlite

import (
	"database/sql"
	"time"

	"github.com/Bowl42/maxx-next/internal/domain"
)

type ProjectRepository struct {
	db *DB
}

func NewProjectRepository(db *DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

func (r *ProjectRepository) Create(p *domain.Project) error {
	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now

	result, err := r.db.db.Exec(
		`INSERT INTO projects (created_at, updated_at, name) VALUES (?, ?, ?)`,
		p.CreatedAt, p.UpdatedAt, p.Name,
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	p.ID = uint64(id)
	return nil
}

func (r *ProjectRepository) Update(p *domain.Project) error {
	p.UpdatedAt = time.Now()
	_, err := r.db.db.Exec(
		`UPDATE projects SET updated_at = ?, name = ? WHERE id = ?`,
		p.UpdatedAt, p.Name, p.ID,
	)
	return err
}

func (r *ProjectRepository) Delete(id uint64) error {
	_, err := r.db.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

func (r *ProjectRepository) GetByID(id uint64) (*domain.Project, error) {
	row := r.db.db.QueryRow(`SELECT id, created_at, updated_at, name FROM projects WHERE id = ?`, id)
	var p domain.Project
	err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *ProjectRepository) List() ([]*domain.Project, error) {
	rows, err := r.db.db.Query(`SELECT id, created_at, updated_at, name FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []*domain.Project
	for rows.Next() {
		var p domain.Project
		err := rows.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Name)
		if err != nil {
			return nil, err
		}
		projects = append(projects, &p)
	}
	return projects, rows.Err()
}
