// Package ossein provides a small, batteries-included foundation for Go HTTP
// applications while preserving standard library types and conventions.
//
// Applications are built around App:
//
//	app := ossein.New()
//	app.Get("/health", func(ctx *ossein.Context) error {
//		return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
//	}).Named("health")
//
// Ossein includes routing, middleware, typed errors, request binding, validation,
// environment configuration, structured logging, lifecycle hooks, and explicit
// constructor-based dependency wiring. The underlying http.Handler,
// http.Request, http.ResponseWriter, context.Context, error, and slog APIs remain
// directly accessible.
package ossein
