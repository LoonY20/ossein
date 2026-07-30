package main

import (
	"strings"

	ossein "github.com/LoonY20/ossein"
)

type writeUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (r *writeUserRequest) Validate() error {
	validation := ossein.NewValidationError()
	if strings.TrimSpace(r.Name) == "" {
		validation.Add("name", "is required")
	}
	if !strings.Contains(r.Email, "@") {
		validation.Add("email", "must be a valid email address")
	}
	return validation.OrNil()
}
