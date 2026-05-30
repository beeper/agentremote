package connector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func errNoAIChat() bridgev2.MessageStatus {
	return bridgev2.WrapErrorInStatus(errors.New("room is not an AI chat")).
		WithStatus(event.MessageStatusFail).
		WithErrorReason(event.MessageStatusUnsupported).
		WithMessage("This room is not linked to an AI chat. Start a new AI chat or recreate this portal.").
		WithIsCertain(true).
		WithSendNotice(true)
}

func wrapNoAIChatError(format string, args ...any) bridgev2.MessageStatus {
	status := errNoAIChat()
	status.InternalError = fmt.Errorf(format, args...)
	return status
}

func matrixMessageStatusForAIError(err error) bridgev2.MessageStatus {
	status := bridgev2.WrapErrorInStatus(err)
	if status.InternalError == nil {
		status.InternalError = err
	}
	if status.Status != "" && status.ErrorReason != event.MessageStatusGenericError {
		return status
	}

	status.Status = event.MessageStatusRetriable
	status.ErrorReason = event.MessageStatusGenericError
	status.Message = "AI failed to respond"

	lower := strings.ToLower(err.Error())
	httpStatus := errorHTTPStatus(err)
	switch {
	case errors.Is(err, context.Canceled):
		status.Status = event.MessageStatusFail
		status.ErrorReason = event.MessageStatusBridgeUnavailable
		status.Message = "AI request was cancelled"
	case errors.Is(err, context.DeadlineExceeded), isNetworkTimeout(err):
		status.ErrorReason = event.MessageStatusNetworkError
		status.Message = "AI provider request timed out"
	case httpStatus == http.StatusUnauthorized || httpStatus == http.StatusForbidden ||
		strings.Contains(lower, "missing api key") ||
		strings.Contains(lower, "no api key") ||
		strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "forbidden"):
		status.Status = event.MessageStatusFail
		status.ErrorReason = event.MessageStatusNoPermission
		status.Message = "AI provider credentials are missing or invalid"
	case httpStatus == http.StatusTooManyRequests || strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests"):
		status.ErrorReason = event.MessageStatusNetworkError
		status.Message = "AI provider rate limited the request"
	case httpStatus >= 500:
		status.ErrorReason = event.MessageStatusNetworkError
		status.Message = "AI provider is temporarily unavailable"
	case httpStatus >= 400:
		status.Status = event.MessageStatusFail
		status.ErrorReason = event.MessageStatusUnsupported
		status.Message = "AI provider rejected the request"
	case strings.Contains(lower, "does not support") ||
		strings.Contains(lower, "unsupported") ||
		strings.Contains(lower, "unsupported message type") ||
		strings.Contains(lower, "unsupported media type"):
		status.Status = event.MessageStatusFail
		status.ErrorReason = event.MessageStatusUnsupported
		status.Message = err.Error()
	case strings.Contains(lower, "provider") && (strings.Contains(lower, "not found") || strings.Contains(lower, "disabled")):
		status.Status = event.MessageStatusFail
		status.ErrorReason = event.MessageStatusNoPermission
		status.Message = "AI provider is not configured"
	case strings.Contains(lower, "model") && (strings.Contains(lower, "not found") || strings.Contains(lower, "not allowed")):
		status.Status = event.MessageStatusFail
		status.ErrorReason = event.MessageStatusUnsupported
		status.Message = "AI model is not available"
	}
	return status
}

func isNetworkTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func errorHTTPStatus(err error) int {
	for current := err; current != nil; current = errors.Unwrap(current) {
		value := reflect.ValueOf(current)
		if value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		if value.IsValid() && value.Kind() == reflect.Struct {
			field := value.FieldByName("StatusCode")
			if field.IsValid() && field.Kind() == reflect.Int && field.CanInt() {
				return int(field.Int())
			}
		}
	}
	return 0
}
