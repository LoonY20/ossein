package main

import (
	"context"
	"errors"
	"sort"
	"sync"
)

var errUserNotFound = errors.New("user not found")

type userRepository interface {
	all(context.Context) ([]user, error)
	find(context.Context, int64) (user, error)
	create(context.Context, user) (user, error)
	update(context.Context, user) (user, error)
	delete(context.Context, int64) error
}

type memoryUserRepository struct {
	mu     sync.RWMutex
	nextID int64
	users  map[int64]user
}

func newMemoryUserRepository() *memoryUserRepository {
	return &memoryUserRepository{
		nextID: 1,
		users:  make(map[int64]user),
	}
}

func (r *memoryUserRepository) all(context.Context) ([]user, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]user, 0, len(r.users))
	for _, item := range r.users {
		users = append(users, item)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	return users, nil
}

func (r *memoryUserRepository) find(_ context.Context, id int64) (user, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	item, ok := r.users[id]
	if !ok {
		return user{}, errUserNotFound
	}
	return item, nil
}

func (r *memoryUserRepository) create(_ context.Context, item user) (user, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item.ID = r.nextID
	r.nextID++
	r.users[item.ID] = item
	return item, nil
}

func (r *memoryUserRepository) update(_ context.Context, item user) (user, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.users[item.ID]; !ok {
		return user{}, errUserNotFound
	}
	r.users[item.ID] = item
	return item, nil
}

func (r *memoryUserRepository) delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.users[id]; !ok {
		return errUserNotFound
	}
	delete(r.users, id)
	return nil
}
