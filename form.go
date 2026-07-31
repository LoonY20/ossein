package ossein

import (
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
)

// maxFormFields bounds how many distinct fields a urlencoded body may carry. The
// body limit does not bound the parsed result: a body of short empty keys expands
// into a far larger map. The value matches the part limit mime/multipart applies.
const maxFormFields = 1000

// FormBindable is implemented by request types that read themselves from a form.
//
// Binding is an explicit method rather than struct tags, so the request path stays
// free of reflection and the mapping is ordinary Go that a reader can follow:
//
//	func (r *ReplayRequest) BindForm(form *ossein.Form) error {
//		r.Event = form.Required("event")
//		r.Limit = form.Int("limit")
//		return nil
//	}
type FormBindable interface {
	BindForm(*Form) error
}

// Form is the submitted values of a form body plus any uploaded files.
//
// It embeds Values, so the typed accessors are the same ones a query string uses.
// A Form reads only the request body: query-string parameters are deliberately
// excluded, so a field can never be satisfied from the URL.
type Form struct {
	Values

	files map[string][]*multipart.FileHeader
}

// Has reports whether a field was submitted at all, as a value or as a file.
func (f *Form) Has(field string) bool {
	if f.Values.Has(field) {
		return true
	}
	_, ok := f.files[field]
	return ok
}

// File returns the first uploaded file for a field, or nil when it is absent.
// Open the returned header to read the contents.
//
// Filename is chosen by the client. The standard library reduces it to its base
// name, so it cannot escape a directory, but it can still be a surprising value
// such as ".."; sanitise it before using it as a path component.
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
// malformed value is not also reported as a broken application rule, and they take
// precedence over an error the bind method itself returned, since they are what the
// client can act on. When the values bind cleanly and target implements
// Validatable, validation runs automatically.
//
// Multipart parts are held in memory rather than spilled to temporary files, so
// there is nothing to clean up: the in-memory limit is the body limit, and the
// whole body was already capped at it, so no part can exceed what remains. Note
// that this holds two copies of the body, so peak use is about twice the limit for
// a multipart request. The body limit does not bound the size of the parsed form
// itself, which is why the field count is capped separately.
func (c *Context) BindForm(target FormBindable) error {
	if target == nil {
		return BadRequest("invalid_request", "Request target cannot be nil")
	}

	values, files, err := c.parseForm()
	if err != nil {
		return err
	}

	form := &Form{Values: Values{values: values}, files: files}
	return finishBinding(target.BindForm(form), &form.Values, target)
}

// finishBinding applies the shared error precedence: recorded field errors first,
// then an error the bind method returned, then validation.
func finishBinding(bindErr error, values *Values, target any) error {
	if fieldErr := values.Err(); fieldErr != nil {
		return fieldErr
	}
	if bindErr != nil {
		return bindErr
	}
	if validatable, ok := target.(Validatable); ok {
		return validatable.Validate()
	}
	return nil
}

// parseForm applies the body limit and parses the body according to its media
// type. It returns the values and, for a multipart request, the uploaded files.
func (c *Context) parseForm() (url.Values, map[string][]*multipart.FileHeader, error) {
	multipartRequest, err := c.checkFormContentType()
	if err != nil {
		return nil, nil, err
	}

	// Body caches the request body under the configured limit and reinstates a
	// reader, so parsing is bounded and the raw bytes stay available.
	raw, err := c.Body()
	if err != nil {
		return nil, nil, err
	}

	if multipartRequest {
		// The in-memory limit is the body limit, and Body already capped the whole
		// body at it, so no single part can exceed what remains and nothing is
		// spilled to a temporary file. Removing the pre-read above would break
		// that guarantee.
		if err := c.Request.ParseMultipartForm(c.bindLimit()); err != nil {
			return nil, nil, BadRequest("invalid_form", "Request body is not a valid multipart form").
				WithCause(err)
		}
		return c.Request.PostForm, c.Request.MultipartForm.File, nil
	}

	// The body is parsed directly rather than through Request.ParseForm, which only
	// reads bodies for POST, PUT, and PATCH and applies its own 10 MB cap that
	// would shadow a larger WithMaxBindBytes.
	values, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, nil, BadRequest("invalid_form", "Request body is not a valid form").
			WithCause(err)
	}
	if len(values) > maxFormFields {
		return nil, nil, NewHTTPError(
			http.StatusRequestEntityTooLarge,
			"too_many_fields",
			"Request contains too many form fields",
		)
	}

	// Expose the parsed values through the standard field as well, so
	// PostFormValue and a later ParseForm agree with what was bound.
	c.Request.PostForm = values
	return values, nil, nil
}

// checkFormContentType reports whether the request is multipart, rejecting media
// types that are not forms.
func (c *Context) checkFormContentType() (bool, error) {
	contentType := c.Request.Header.Get("Content-Type")
	if contentType == "" {
		return false, unsupportedFormMediaType(nil)
	}

	// A malformed parameter still yields a usable media type, and net/http accepts
	// such requests, so only the type itself decides.
	mediaType, _, err := mime.ParseMediaType(contentType)
	if mediaType == "" && err != nil {
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
