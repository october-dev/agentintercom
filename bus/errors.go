package bus

import (
	"errors"
	"net/http"
)

type ErrorCode string

const (
	CodeInvalidArgument  ErrorCode = "INVALID_ARGUMENT"
	CodeUnauthenticated  ErrorCode = "UNAUTHENTICATED"
	CodePermissionDenied ErrorCode = "PERMISSION_DENIED"
	CodeNotFound         ErrorCode = "NOT_FOUND"
	CodeMethodNotAllowed ErrorCode = "METHOD_NOT_ALLOWED"
	CodeConflict         ErrorCode = "CONFLICT"
	CodeBackpressure     ErrorCode = "BACKPRESSURE"
	CodeInternal         ErrorCode = "INTERNAL"
)

type BusError struct {
	Code    ErrorCode
	Message string
}

func (e *BusError) Error() string { return e.Message }

func Errorf(code ErrorCode, message string) error {
	return &BusError{Code: code, Message: message}
}

func AsBusError(err error) *BusError {
	var target *BusError
	if errors.As(err, &target) {
		return target
	}
	if databaseErrorState(err) == "55P03" {
		return &BusError{Code: CodeBackpressure, Message: "Workspace is busy; retry shortly"}
	}
	return &BusError{Code: CodeInternal, Message: "Internal Agent Intercom error"}
}

// SQLSTATE is safe operational context; driver messages, queries and arguments
// may contain private workspace data or credentials and must not be logged.
func databaseErrorState(err error) string {
	var state interface{ SQLState() string }
	if !errors.As(err, &state) {
		return ""
	}
	code := state.SQLState()
	if len(code) != 5 {
		return ""
	}
	for _, c := range code {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return code
}

func ErrorStatus(err error) int {
	switch AsBusError(err).Code {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CodeConflict:
		return http.StatusConflict
	case CodeBackpressure:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}
