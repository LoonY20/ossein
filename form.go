package ossein

import (
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// FormBindable is implemented by request types that read themselves from a form.
//
// Binding is an explicit method rather than struct tags, so the request path
// stays free of reflection and the mapping is ordinary Go that a reader can
// follow:
//
//	func (r *ReplayRequest) BindForm(form *ossein.Form) error {
//		r.Event = form.Required("event")
//		r.Limit = form.Int("limit")
//		return nil
//	}
type FormBindable interface {
	BindForm(*Form) error
}

// Form provides typed access to submitted form values and files.
//
// Accessors record a field-level error instead of returning one, so a bind
// method reads as a list of assignments. Values that are absent yield the zero
// value; values that are present but malformed are reported. Call Err, or let
// Context.BindForm do it, to collect the result.
type Form struct {
	values url.Values
	files  map[string][]*multipart.FileHeader
	errs   *ValidationError
}

// Values returns the submitted values, for cases the accessors do not cover.
func (f *Form) Values() url.Values {
	return f.values
}

// Has reports whether a field was submitted at all, which distinguishes an
// absent field from one submitted empty.
func (f *Form) Has(field string) bool {
	_, ok := f.values[field]
	return ok
}

// String returns a field value, or "" when it is absent.
func (f *Form) String(field string) string {
	return f.values.Get(field)
}

// Strings returns every value submitted for a repeated field.
func (f *Form) Strings(field string) []string {
	return f.values[field]
}

// Required returns a trimmed field value, recording an error when it is absent
// or blank.
func (f *Form) Required(field string) string {
	value := strings.TrimSpace(f.values.Get(field))
	if value == "" {
		f.AddError(field, "is required")
	}
	return value
}

// Int returns a field as an int. An absent field yields zero; a malformed one is
// reported.
func (f *Form) Int(field string) int {
	return int(f.parseInt(field, strconv.IntSize))
}

// Int64 returns a field as an int64.
func (f *Form) Int64(field string) int64 {
	return f.parseInt(field, 64)
}

func (f *Form) parseInt(field string, bits int) int64 {
	raw := strings.TrimSpace(f.values.Get(field))
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(raw, 10, bits)
	if err != nil {
		f.AddError(field, "must be a whole number")
		return 0
	}
	return parsed
}

// Float64 returns a field as a float64.
func (f *Form) Float64(field string) float64 {
	raw := strings.TrimSpace(f.values.Get(field))
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		f.AddError(field, "must be a number")
		return 0
	}
	return parsed
}

// Bool returns a field as a bool, accepting the forms strconv.ParseBool does
// plus the "on" that unchecked HTML checkboxes omit and checked ones send.
func (f *Form) Bool(field string) bool {
	raw := strings.TrimSpace(f.values.Get(field))
	if raw == "" {
		return false
	}
	if strings.EqualFold(raw, "on") {
		return true
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		f.AddError(field, "must be true or false")
		return false
	}
	return parsed
}

// File returns the first uploaded file for a field, or nil when it is absent.
// Open the returned header to read the contents.
func (f *Form) File(field string) *multipart.FileHeader {
	headers := f.files[field]
	if len(headers) == 0 {
		return nil
	}
	return headers[0]
}

// RequiredFile is File, recording an error when no file was uploaded.
func (f *Form) RequiredFile(field string) *multipart.FileHeader {
	header := f.File(field)
	if header == nil {
		f.AddError(field, "a file is required")
	}
	return header
}

// Files returns every file uploaded for a field.
func (f *Form) Files(field string) []*multipart.FileHeader {
	return f.files[field]
}

// AddError records an application rule failure against a field, so a bind method
// can add checks alongside the accessors.
func (f *Form) AddError(field, message string) {
	if f.errs == nil {
		f.errs = NewValidationError()
	}
	f.errs.Add(field, message)
}

// Err returns the recorded field errors, or nil when there are none.
func (f *Form) Err() error {
	return f.errs.OrNil()
}

// BindForm parses a form request into target.
//
// The Content-Type must be application/x-www-form-urlencoded or
// multipart/form-data. Anything else, including an absent Content-Type, is
// rejected with 415. BindJSON tolerates an absent Content-Type because decoding
// validates the format on the way through; parsing a query string almost never
// fails, so an unlabelled body would bind as silently empty fields instead. The
// body is limited to the application's WithMaxBindBytes setting, matching
// BindJSON.
//
// Field errors recorded by the accessors are reported before Validate runs, so a
// malformed value is not also reported as a broken application rule. When the
// values bind cleanly and target implements Validatable, validation runs
// automatically.
//
// Multipart parts are held in memory: the in-memory limit is the same body limit,
// so no part is ever spilled to a temporary file and there is nothing to clean up.
func (c *Context) BindForm(target FormBindable) error {
	if target == nil {
		return BadRequest("invalid_request", "Request target cannot be nil")
	}

	multipartForm, err := c.parseForm()
	if err != nil {
		return err
	}

	// A successful parse always initialises PostForm, so no nil guard is needed.
	form := &Form{values: c.Request.PostForm}
	if multipartForm != nil {
		form.files = multipartForm.File
	}

	if err := target.BindForm(form); err != nil {
		return err
	}
	if err := form.Err(); err != nil {
		return err
	}

	if validatable, ok := target.(Validatable); ok {
		return validatable.Validate()
	}
	return nil
}

// parseForm applies the body limit and hands the request to the standard library
// parser matching the media type. It returns the multipart form when the request
// carried one.
func (c *Context) parseForm() (*multipart.Form, error) {
	multipartRequest, err := c.checkFormContentType()
	if err != nil {
		return nil, err
	}

	// Body caches the request body under the configured limit and reinstates a
	// reader, so the standard library parsers below see a limited body and the
	// raw bytes stay available.
	if _, err := c.Body(); err != nil {
		return nil, err
	}

	if multipartRequest {
		if err := c.Request.ParseMultipartForm(c.bindLimit()); err != nil {
			return nil, BadRequest("invalid_form", "Request body is not a valid multipart form").
				WithCause(err)
		}
		return c.Request.MultipartForm, nil
	}

	if err := c.Request.ParseForm(); err != nil {
		return nil, BadRequest("invalid_form", "Request body is not a valid form").
			WithCause(err)
	}
	return nil, nil
}

// checkFormContentType reports whether the request is multipart, rejecting media
// types that are not forms.
func (c *Context) checkFormContentType() (bool, error) {
	contentType := c.Request.Header.Get("Content-Type")
	if contentType == "" {
		return false, unsupportedFormMediaType(nil)
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false, unsupportedFormMediaType(err)
	}

	switch mediaType {
	case "application/x-www-form-urlencoded":
		return false, nil
	case "multipart/form-data":
		return true, nil
	default:
		return false, unsupportedFormMediaType(nil)
	}
}

func unsupportedFormMediaType(cause error) error {
	return NewHTTPError(
		http.StatusUnsupportedMediaType,
		"unsupported_media_type",
		"Request Content-Type must be application/x-www-form-urlencoded or multipart/form-data",
	).WithCause(cause)
}
