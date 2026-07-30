package main

import ossein "github.com/LoonY20/ossein"

func newApplication() (*ossein.App, error) {
	app := ossein.New()

	if err := ossein.ProvideAs[userRepository](app, newMemoryUserRepository); err != nil {
		return nil, err
	}
	if err := app.Provide(newUserService); err != nil {
		return nil, err
	}
	if err := app.Provide(newUserHandlers); err != nil {
		return nil, err
	}

	handlers, err := ossein.Resolve[*userHandlers](app)
	if err != nil {
		return nil, err
	}
	registerRoutes(app, handlers)

	return app, nil
}

func registerRoutes(app *ossein.App, handlers *userHandlers) {
	app.Group("/users", func(users *ossein.Router) {
		users.Get("", handlers.index).Named("users.index")
		users.Post("", handlers.store).Named("users.store")
		users.Get("/{id}", handlers.show).Named("users.show")
		users.Put("/{id}", handlers.update).Named("users.update")
		users.Delete("/{id}", handlers.destroy).Named("users.destroy")
	})
}
