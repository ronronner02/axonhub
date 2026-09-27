package responses

import (
	"net/http"

	"github.com/looplj/axonhub/llm"
)

// 同时保留标准平铺错误、供应商嵌套错误及 response.failed 的失败语义。
func responseStreamError(event *StreamEvent) *llm.ResponseError {
	detail := llm.ErrorDetail{Code: event.Code, Message: event.Message}
	if event.Param != nil {
		detail.Param = *event.Param
	}
	if event.Error != nil {
		nested := *event.Error
		if nested.Code == "" {
			nested.Code = detail.Code
		}
		if nested.Message == "" {
			nested.Message = detail.Message
		}
		if nested.Param == "" {
			nested.Param = detail.Param
		}
		detail = nested
	} else if event.Response != nil && event.Response.Error != nil {
		upstream := event.Response.Error
		detail = llm.ErrorDetail{Code: upstream.Code, Message: upstream.Message, Type: upstream.Type}
	}
	if detail.Message == "" {
		detail.Message = "upstream response failed"
	}
	if detail.Type == "" {
		detail.Type = "upstream_error"
	}
	status := http.StatusBadGateway
	switch detail.Code {
	case "rate_limit_exceeded", "insufficient_quota":
		status = http.StatusTooManyRequests
	case "invalid_encrypted_content", "invalid_request_error":
		status = http.StatusBadRequest
	}
	if detail.Type == "too_many_requests" || detail.Type == "rate_limit_error" {
		status = http.StatusTooManyRequests
	}
	return &llm.ResponseError{StatusCode: status, Detail: detail}
}
