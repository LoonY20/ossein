package main

// user is deliberately small: the example focuses on Ossein application flow,
// not persistence or domain modeling.
type user struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
