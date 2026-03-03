package cached

import (
	"sync"

	"github.com/Bowl42/maxx-next/internal/domain"
	"github.com/Bowl42/maxx-next/internal/repository"
)

type ProjectRepository struct {
	repo  repository.ProjectRepository
	cache map[uint64]*domain.Project
	mu    sync.RWMutex
}

func NewProjectRepository(repo repository.ProjectRepository) *ProjectRepository {
	return &ProjectRepository{
		repo:  repo,
		cache: make(map[uint64]*domain.Project),
	}
}

func (r *ProjectRepository) Load() error {
	list, err := r.repo.List()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range list {
		r.cache[p.ID] = p
	}
	return nil
}

func (r *ProjectRepository) Create(p *domain.Project) error {
	if err := r.repo.Create(p); err != nil {
		return err
	}
	r.mu.Lock()
	r.cache[p.ID] = p
	r.mu.Unlock()
	return nil
}

func (r *ProjectRepository) Update(p *domain.Project) error {
	if err := r.repo.Update(p); err != nil {
		return err
	}
	r.mu.Lock()
	r.cache[p.ID] = p
	r.mu.Unlock()
	return nil
}

func (r *ProjectRepository) Delete(id uint64) error {
	if err := r.repo.Delete(id); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.cache, id)
	r.mu.Unlock()
	return nil
}

func (r *ProjectRepository) GetByID(id uint64) (*domain.Project, error) {
	r.mu.RLock()
	if p, ok := r.cache[id]; ok {
		r.mu.RUnlock()
		return p, nil
	}
	r.mu.RUnlock()
	return r.repo.GetByID(id)
}

func (r *ProjectRepository) List() ([]*domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]*domain.Project, 0, len(r.cache))
	for _, p := range r.cache {
		list = append(list, p)
	}
	return list, nil
}
