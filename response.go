package ossein

import (
	"encoding/json"
	"net/http"
)

// JSON writes value as a JSON response with the provided status code.
// The value is encoded before headers are committed, so serialization errors
// can still be handled by the caller.
func JSON(w http.ResponseWriter, status int, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(append(payload, '\n'))
	return err
}
