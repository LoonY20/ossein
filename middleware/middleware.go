// Package middleware provides the standard middleware an HTTP service needs but
// should not have to write: panic recovery, access logging, and security headers.
//
// Every middleware here is an ossein.Middleware, which is the standard library's
// own func(http.Handler) http.Handler, so it composes with middleware from
// anywhere else. Responses are rendered through the application's ErrorHandler,
// so a service that customises its error shape gets that shape here too.
//
// Register them application-wide, outermost first:
//
//	app.Use(
//		middleware.Recover(),
//		middleware.AccessLog(),
//		middleware.SecurityHeaders(),
//	)
package middleware
