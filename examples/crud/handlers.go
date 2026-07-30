package main

import (
	"errors"
	"net/http"
	"strconv"

	ossein "github.com/LoonY20/ossein"
)

type userHandlers struct {
	users *userService
}

func newUserHandlers(users *userService) *userHandlers {
	return &userHandlers{users: users}
}

func (h *userHandlers) index(ctx *ossein.Context) error {
	users, err := h.users.all(ctx.Context())
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, users)
}

func (h *userHandlers) show(ctx *ossein.Context) error {
	id, err := userID(ctx)
	if err != nil {
		return err
	}
	item, err := h.users.find(ctx.Context(), id)
	if err != nil {
		return userError(err)
	}
	return ctx.JSON(http.StatusOK, item)
}

func (h *userHandlers) store(ctx *ossein.Context) error {
	var request writeUserRequest
	if err := ctx.BindJSON(&request); err != nil {
		return err
	}
	item, err := h.users.create(ctx.Context(), request)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, item)
}

func (h *userHandlers) update(ctx *ossein.Context) error {
	id, err := userID(ctx)
	if err != nil {
		return err
	}
	var request writeUserRequest
	if err := ctx.BindJSON(&request); err != nil {
		return err
	}
	item, err := h.users.update(ctx.Context(), id, request)
	if err != nil {
		return userError(err)
	}
	return ctx.JSON(http.StatusOK, item)
}

func (h *userHandlers) destroy(ctx *ossein.Context) error {
	id, err := userID(ctx)
	if err != nil {
		return err
	}
	if err := h.users.delete(ctx.Context(), id); err != nil {
		return userError(err)
	}
	return ctx.NoContent(http.StatusNoContent)
}

func userID(ctx *ossein.Context) (int64, error) {
	id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, ossein.BadRequest("invalid_user_id", "User ID must be a positive integer")
	}
	return id, nil
}

func userError(err error) error {
	if errors.Is(err, errUserNotFound) {
		return ossein.NotFound("user_not_found", "User not found")
	}
	return err
}
