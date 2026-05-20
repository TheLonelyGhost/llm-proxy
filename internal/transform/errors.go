package transform

import "errors"

// ErrInvalidJSON indicates the response body contains invalid JSON.
var (
	ErrInvalidJSON      = errors.New("invalid JSON in response body")
	ErrMissingField     = errors.New("required field missing from response")
	ErrInvalidFieldType = errors.New("field has invalid type")
)
