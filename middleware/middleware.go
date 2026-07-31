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
//		middleware.AccessLog(),
//		middleware.CORS(corsOptions),
//		middleware.Recover(),
//		middleware.SecurityHeaders(),
//		middleware.Timeout(15*time.Second),
//	)
//
// The order matters in three places. AccessLog goes outermost, because a middleware
// only observes a status written below it, so any other position logs a panicking
// request with the status it had before recovery rather than the 500 the client
// received. CORS goes inside AccessLog but above everything else: it answers a
// preflight without reaching what follows, so a log registered below never sees one.
// Timeout goes inside Recover, so a panic it forwards from the handler's goroutine is
// still caught.
package middleware
