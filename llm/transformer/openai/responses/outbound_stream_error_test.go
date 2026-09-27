package responses_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

func TestResponsesStreamPreservesUpstreamErrors(t *testing.T) {
	cases := []struct{ name, kind, data string }{
		{"SSE事件名补齐类型", "error", `{"error":{"type":"too_many_requests","code":"rate_limit_exceeded","message":"token rate limit exceeded"}}`},
		{"nested_rate_limit", "error", `{"type":"error","error":{"type":"too_many_requests","code":"rate_limit_exceeded","message":"token rate limit exceeded","param":null},"sequence_number":1}`},
		{"flat_rate_limit", "error", `{"type":"error","code":"rate_limit_exceeded","message":"token rate limit exceeded","sequence_number":1}`},
		{"failed_response_rate_limit", "response.failed", `{"type":"response.failed","response":{"id":"resp_fixture","object":"response","status":"failed","model":"MODEL","created_at":1700000000,"output":[],"error":{"code":"rate_limit_exceeded","message":"token rate limit exceeded"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := responses.NewOutboundTransformer("https://example.invalid", "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			stream, err := tr.TransformStream(context.Background(), nil, streams.SliceStream([]*httpclient.StreamEvent{
				{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"resp_fixture","object":"response","status":"in_progress","model":"MODEL","created_at":1700000000,"output":[]}}`)},
				{Type: tc.kind, Data: []byte(tc.data)},
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for stream.Next() {
				if current := stream.Current(); current != nil && current.Object == "[DONE]" {
					t.Error("失败流被输出为成功终止")
				}
			}
			var re *llm.ResponseError
			if !errors.As(stream.Err(), &re) {
				t.Fatalf("预期保留上游错误，实际为 %v", stream.Err())
			}
			if re.Detail.Code != "rate_limit_exceeded" {
				t.Errorf("错误码丢失: %q", re.Detail.Code)
			}
			if re.Detail.Message != "token rate limit exceeded" {
				t.Errorf("错误消息丢失: %q", re.Detail.Message)
			}
			if re.StatusCode != http.StatusTooManyRequests {
				t.Errorf("限流状态未映射: %d", re.StatusCode)
			}
			if stream.Next() {
				t.Error("错误之后不应恢复读取或输出 DONE")
			}
		})
	}
}

func TestResponsesRateLimitReachesClientAsFailedEvent(t *testing.T) {
	outbound, err := responses.NewOutboundTransformer("https://example.invalid", "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	source, err := outbound.TransformStream(t.Context(), nil, streams.SliceStream([]*httpclient.StreamEvent{
		{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"resp_fixture","object":"response","status":"in_progress","created_at":1700000000,"model":"MODEL","output":[]}}`)},
		{Type: "error", Data: []byte(`{"type":"error","error":{"type":"too_many_requests","code":"rate_limit_exceeded","message":"token rate limit exceeded"}}`)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	client, err := responses.NewInboundTransformer().TransformStream(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	failed := false
	for client.Next() {
		frame := client.Current()
		if frame == nil {
			continue
		}
		var event responses.StreamEvent
		if err := json.Unmarshal(frame.Data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == responses.StreamEventTypeResponseCompleted {
			t.Fatal("失败被伪装为完成")
		}
		if event.Type == responses.StreamEventTypeResponseFailed {
			failed = true
			if event.Response == nil || event.Response.Error == nil {
				t.Fatal("缺少失败原因")
			}
			if event.Response.Error.Code != "rate_limit_exceeded" || event.Response.Error.Message != "token rate limit exceeded" || event.Response.Error.Type != "too_many_requests" {
				t.Fatalf("错误语义丢失: %+v", event.Response.Error)
			}
		}
	}
	if !failed {
		t.Fatal("客户端缺少 response.failed 事件")
	}
}
