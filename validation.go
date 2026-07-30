package ossein

// Validatable can be implemented by request DTOs that want BindJSON to run
// explicit application validation after decoding.
type Validatable interface {
	Validate() error
}

// ValidationError contains field-level validation messages.
type ValidationError struct {
	Fields map[string][]string `json:"fields"`
}

// NewValidationError creates an empty validation error collection.
func NewValidationError() *ValidationError {
	return &ValidationError{Fields: make(map[string][]string)}
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return "validation failed"
}

// Add appends a validation message for field and returns the same collection.
func (e *ValidationError) Add(field, message string) *ValidationError {
	if e.Fields == nil {
		e.Fields = make(map[string][]string)
	}
	e.Fields[field] = append(e.Fields[field], message)
	return e
}

// OrNil returns nil when no validation messages were added.
func (e *ValidationError) OrNil() error {
	if e == nil || len(e.Fields) == 0 {
		return nil
	}
	return e
}
