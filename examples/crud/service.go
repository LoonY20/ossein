package main

import "context"

type userService struct {
	users userRepository
}

func newUserService(users userRepository) *userService {
	return &userService{users: users}
}

func (s *userService) all(ctx context.Context) ([]user, error) {
	return s.users.all(ctx)
}

func (s *userService) find(ctx context.Context, id int64) (user, error) {
	return s.users.find(ctx, id)
}

func (s *userService) create(ctx context.Context, request writeUserRequest) (user, error) {
	return s.users.create(ctx, user{Name: request.Name, Email: request.Email})
}

func (s *userService) update(ctx context.Context, id int64, request writeUserRequest) (user, error) {
	return s.users.update(ctx, user{ID: id, Name: request.Name, Email: request.Email})
}

func (s *userService) delete(ctx context.Context, id int64) error {
	return s.users.delete(ctx, id)
}
