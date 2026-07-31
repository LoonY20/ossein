package ossein

import (
	"net/http"
	"net/url"
)

// QueryBindable is implemented by request types that read themselves from the
// query string.
//
// Like form binding, the mapping is an explicit method rather than struct tags, so
// the request path stays free of reflection:
//
//	func (q *ListQuery) BindQuery(values *ossein.Values) error {
//		q.Page = values.IntOr("page", 1)
//		q.Search = values.String("q")
//		return nil
//	}
type QueryBindable interface {
	BindQuery(*Values) error
}

// Query returns the parsed query string for ad-hoc reads, when a handler wants one
// or two parameters and not a request type.
//
// The result is parsed once and reused. Accessor errors are recorded rather than
// returned, so a handler that uses the typed accessors should return Err:
//
//	query, err := c.Query()
//	if err != nil {
//		return err
//	}
//	page := query.Int("page")
//	if err := query.Err(); err != nil {
//		return err
//	}
//
// A malformed query string is reported as a 400 rather than binding as silently
// missing fields.
func (c *Context) Query() (*Values, error) {
	if c.query != nil {
		return c.query, nil
	}
	if c.queryErr != nil {
		return nil, c.queryErr
	}

	// url.Values from Request.URL.Query() silently drops unparseable pairs, so the
	// raw query is parsed here to report the failure instead.
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		c.queryErr = BadRequest("invalid_query", "Query string could not be parsed").
			WithCause(err)
		return nil, c.queryErr
	}
	// The field count is capped for the same reason a form body's is: short empty
	// keys expand into a far larger map than the request that carried them.
	if len(values) > maxFormFields {
		c.queryErr = NewHTTPError(
			http.StatusRequestEntityTooLarge,
			"too_many_fields",
			"Query string contains too many fields",
		)
		return nil, c.queryErr
	}

	c.query = NewValues(values)
	return c.query, nil
}

// BindQuery parses the query string into target.
//
// Field errors recorded by the accessors are reported before Validate runs, and
// they take precedence over an error the bind method itself returned. When the
// values bind cleanly and target implements Validatable, validation runs
// automatically.
//
// Only the query string is read; a form body never satisfies a query field.
func (c *Context) BindQuery(target QueryBindable) error {
	if target == nil {
		return BadRequest("invalid_request", "Request target cannot be nil")
	}

	values, err := c.Query()
	if err != nil {
		return err
	}

	return finishBinding(target.BindQuery(values), values, target)
}
