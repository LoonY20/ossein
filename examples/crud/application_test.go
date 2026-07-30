package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserCRUD(t *testing.T) {
	app, err := newApplication()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("stop application: %v", err)
		}
	})

	assertStatus(t, request(t, app, http.MethodPost, "/users", map[string]string{
		"name": "", "email": "invalid",
	}), http.StatusUnprocessableEntity)

	created := request(t, app, http.MethodPost, "/users", map[string]string{
		"name": "Erik", "email": "erik@example.com",
	})
	assertStatus(t, created, http.StatusCreated)
	var createdUser user
	decodeResponse(t, created, &createdUser)
	if createdUser.ID != 1 || createdUser.Name != "Erik" {
		t.Fatalf("created user = %#v", createdUser)
	}

	index := request(t, app, http.MethodGet, "/users", nil)
	assertStatus(t, index, http.StatusOK)
	var users []user
	decodeResponse(t, index, &users)
	if len(users) != 1 || users[0] != createdUser {
		t.Fatalf("users = %#v", users)
	}

	show := request(t, app, http.MethodGet, "/users/1", nil)
	assertStatus(t, show, http.StatusOK)

	updated := request(t, app, http.MethodPut, "/users/1", map[string]string{
		"name": "Erik Z", "email": "erik.z@example.com",
	})
	assertStatus(t, updated, http.StatusOK)
	var updatedUser user
	decodeResponse(t, updated, &updatedUser)
	if updatedUser.Name != "Erik Z" {
		t.Fatalf("updated user = %#v", updatedUser)
	}

	assertStatus(t, request(t, app, http.MethodDelete, "/users/1", nil), http.StatusNoContent)
	assertStatus(t, request(t, app, http.MethodGet, "/users/1", nil), http.StatusNotFound)
	assertStatus(t, request(t, app, http.MethodGet, "/users/nope", nil), http.StatusBadRequest)
}

func TestUserCRUDRejectsUnknownJSONFields(t *testing.T) {
	app, err := newApplication()
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, app, http.MethodPost, "/users", map[string]any{
		"name": "Erik", "email": "erik@example.com", "admin": true,
	})
	assertStatus(t, response, http.StatusBadRequest)
}

func TestUserRoutesAreNamed(t *testing.T) {
	app, err := newApplication()
	if err != nil {
		t.Fatal(err)
	}
	path, err := app.URL("users.show", map[string]string{"id": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/users/42" {
		t.Fatalf("path = %q", path)
	}
	if len(app.Routes()) != 5 {
		t.Fatalf("routes = %#v", app.Routes())
	}
}

func request(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &payload)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if response.Code != expected {
		t.Fatalf("status = %d, expected %d, body = %s", response.Code, expected, response.Body.String())
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
