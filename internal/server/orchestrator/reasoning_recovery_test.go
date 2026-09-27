package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/looplj/axonhub/internal/contexts"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/requestexecution"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

const recoveryRequestBody = `{"model":"gpt-4","stream":false,"store":false,"reasoning":{"effort":"medium"},"input":[{"role":"user","content":"Continue the local fixture"},{"type":"reasoning","id":"rs_fixture","encrypted_content":"fixture-ciphertext","summary":[]},{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"check","arguments":"{\"id\":9007199254740993}"},{"type":"function_call_output","call_id":"call_fixture","output":"{\"id\":9007199254740993,\"encrypted_content\":\"tool-data\"}"},{"role":"user","content":"Continue"}],"tools":[{"type":"function","name":"check","parameters":{"type":"object","properties":{"id":{"type":"integer"}}}}]}`

const recoveryResponseBody = `{"id":"resp_fixture","object":"response","status":"completed","model":"gpt-6-astra","output":[{"type":"message","id":"msg_fixture","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`

type reasoningRecoveryExecutor struct {
	requests          []*httpclient.Request
	acceptHistory     bool
	alwaysFail        bool
	errorBody         string
	afterContentError bool
	nestedRateLimit   bool
	onCall            func()
}

func (e *reasoningRecoveryExecutor) Do(_ context.Context, request *httpclient.Request) (*httpclient.Response, error) {
	copy := *request
	copy.Body = bytes.Clone(request.Body)
	e.requests = append(e.requests, &copy)
	if e.onCall != nil {
		e.onCall()
	}
	if e.alwaysFail || (!e.acceptHistory && gjson.GetBytes(request.Body, `input.#(type=="reasoning")`).Exists()) {
		body := e.errorBody
		if body == "" {
			body = `{"error":{"message":"Fixture encrypted history rejected","type":"invalid_request_error","code":"invalid_encrypted_content"}}`
		}
		return nil, &httpclient.Error{
			StatusCode: http.StatusBadRequest,
			Body:       []byte(body),
		}
	}
	return &httpclient.Response{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(recoveryResponseBody),
	}, nil
}

func (e *reasoningRecoveryExecutor) DoStream(ctx context.Context, request *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	response, err := e.Do(ctx, request)
	if err != nil {
		return nil, err
	}
	if e.nestedRateLimit {
		return streams.SliceStream([]*httpclient.StreamEvent{
			{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"resp_limit","object":"response","status":"in_progress","model":"gpt-6-astra","created_at":1700000000,"output":[]}}`)},
			{Type: "error", Data: []byte(`{"type":"error","error":{"type":"too_many_requests","code":"rate_limit_exceeded","message":"fixture token rate limit exceeded"}}`)},
			{Type: "response.failed", Data: []byte(`{"type":"response.failed","response":{"id":"resp_limit","object":"response","status":"failed","error":{"code":"rate_limit_exceeded","message":"fixture token rate limit exceeded"}}}`)},
		}), nil
	}
	if e.afterContentError {
		return streams.SliceStream([]*httpclient.StreamEvent{
			{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_fixture","delta":"first"}`)},
			{Type: "error", Data: []byte(`{"type":"error","code":"invalid_encrypted_content","message":"Fixture late error"}`)},
		}), nil
	}
	return streams.SliceStream([]*httpclient.StreamEvent{{
		Type: "response.completed",
		Data: []byte(`{"type":"response.completed","response":` + string(response.Body) + `}`),
	}}), nil
}

func newReasoningRecoveryOrchestrator(t *testing.T, executor *reasoningRecoveryExecutor) (context.Context, *ent.Client, *ChatCompletionOrchestrator, *biz.Channel) {
	t.Helper()
	ctx, client := setupTest(t)
	project := createTestProject(t, ctx, client)
	ctx = contexts.WithProjectID(ctx, project.ID)
	ch := createTestChannel(t, ctx, client)
	ch.Settings = &objects.ChannelSettings{
		PassThroughBody: lo.ToPtr(true),
		RateLimit: &objects.ChannelRateLimit{
			RPM: lo.ToPtr(int64(10)), MaxConcurrent: lo.ToPtr(int64(1)), QueueTimeoutMs: lo.ToPtr(int64(100)),
		},
	}
	outbound, err := responses.NewOutboundTransformer("https://fixture.invalid/v1", "fixture-token")
	require.NoError(t, err)
	selected := &biz.Channel{Channel: ch, Outbound: outbound}
	selector := &staticChannelSelector{candidates: []*ChannelModelsCandidate{{
		Channel: selected,
		Models:  []biz.ChannelModelEntry{{RequestModel: "gpt-4", ActualModel: "gpt-6-astra"}},
	}}}
	processor := newTestOrchestrator(t, selector, client, executor)
	processor.Inbound = responses.NewInboundTransformer()
	processor.rateLimitTracker = NewChannelRequestTracker()
	processor.SystemService.CompatibilityConfig.InvalidEncryptedContentRecovery = true
	require.NoError(t, processor.SystemService.SetRetryPolicy(ctx, &biz.RetryPolicy{
		Enabled:                 true,
		MaxSingleChannelRetries: 1,
		LoadBalancerStrategy:    biz.LoadBalancerStrategyAdaptive,
	}))
	return ctx, client, processor, selected
}

func TestReasoningRecovery_UsesNormalAttempts(t *testing.T) {
	assertSuccessfulReasoningRecovery(t, true, false)
}

func TestReasoningRecovery_StreamingAndTransformedRequests(t *testing.T) {
	for _, tt := range []struct{ passThrough, stream bool }{{false, false}, {true, true}, {false, true}} {
		t.Run(fmt.Sprintf("passthrough_%v_stream_%v", tt.passThrough, tt.stream), func(t *testing.T) {
			assertSuccessfulReasoningRecovery(t, tt.passThrough, tt.stream)
		})
	}
}

func recoveryHTTPRequest(t *testing.T, stream bool) *httpclient.Request {
	t.Helper()
	body, err := sjson.Set(recoveryRequestBody, "stream", stream)
	require.NoError(t, err)
	// 非透传转换器会过滤未知签名；此处使用其识别的格式前缀，不包含真实密文。
	body, err = sjson.Set(body, "input.1.encrypted_content", "gAAAA_fixture_ciphertext")
	require.NoError(t, err)
	return &httpclient.Request{
		Method: http.MethodPost, URL: "/v1/responses",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(body),
	}
}

func assertSuccessfulReasoningRecovery(t *testing.T, passThrough, stream bool) {
	t.Helper()
	executor := &reasoningRecoveryExecutor{}
	ctx, client, processor, selected := newReasoningRecoveryOrchestrator(t, executor)
	selected.Settings.PassThroughBody = lo.ToPtr(passThrough)
	executor.onCall = func() {
		inFlight, waiting, ok := processor.channelLimiterManager.Stats(selected.ID)
		require.True(t, ok)
		require.Equal(t, 1, inFlight, "每次出站都必须重新获得并发槽位")
		require.Zero(t, waiting)
	}
	request := recoveryHTTPRequest(t, stream)
	original := bytes.Clone(request.Body)
	result, err := processor.Process(ctx, request)
	require.NoError(t, err, "失效密文应在正常重试预算内恢复，而不是直接返回 400")
	if stream {
		require.NotNil(t, result.ChatCompletionStream)
		defer result.ChatCompletionStream.Close()
		for result.ChatCompletionStream.Next() {
			require.NotNil(t, result.ChatCompletionStream.Current())
		}
		require.NoError(t, result.ChatCompletionStream.Err())
		require.NoError(t, result.ChatCompletionStream.Close())
	} else {
		require.NotNil(t, result.ChatCompletion)
	}
	require.Len(t, executor.requests, 2)
	require.Equal(t, int64(2), processor.rateLimitTracker.GetRequestCount(selected.ID))
	require.Equal(t, original, request.Body, "原始请求应保留")
	waitForRecoveryDrain(t, ctx, client, processor, selected)
	inFlight, waiting, _ := processor.channelLimiterManager.Stats(selected.ID)
	require.Zero(t, inFlight)
	require.Zero(t, waiting)
	require.True(t, gjson.GetBytes(executor.requests[0].Body, `input.#(type=="reasoning")`).Exists())
	require.False(t, gjson.GetBytes(executor.requests[1].Body, `input.#(type=="reasoning")`).Exists())
	require.Equal(t, "medium", gjson.GetBytes(executor.requests[1].Body, "reasoning.effort").String())
	for _, kind := range []string{"function_call", "function_call_output"} {
		path := `input.#(type=="` + kind + `")`
		require.Equal(t, "call_fixture", gjson.GetBytes(executor.requests[1].Body, path+".call_id").String())
	}

	executions, err := client.RequestExecution.Query().Order(ent.Asc(requestexecution.FieldID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, executions, 2, "每次实际出站都应有独立执行记录")
	require.Equal(t, requestexecution.StatusFailed, executions[0].Status)
	require.Equal(t, requestexecution.StatusCompleted, executions[1].Status)
	for i, execution := range executions {
		require.JSONEq(t, string(executor.requests[i].Body), string(execution.RequestBody))
		require.Equal(t, passThrough, execution.PassThroughApplied)
		require.Equal(t, "gpt-6-astra", execution.ModelID)
	}
}

func waitForRecoveryDrain(t *testing.T, ctx context.Context, client *ent.Client, processor *ChatCompletionOrchestrator, selected *biz.Channel) {
	t.Helper()
	// SSE 透传的后台消费负责最终落库，关闭测试数据库前等待它完成。
	require.Eventually(t, func() bool {
		processing, err := client.RequestExecution.Query().Where(requestexecution.StatusEQ(requestexecution.StatusProcessing)).Count(ctx)
		inFlight, waiting, _ := processor.channelLimiterManager.Stats(selected.ID)
		return err == nil && processing == 0 && inFlight == 0 && waiting == 0
	}, 2*time.Second, 5*time.Millisecond)
}

func TestReasoningRecovery_GatesAndOnceOnly(t *testing.T) {
	for _, tt := range []struct {
		name         string
		enabled      bool
		retryEnabled bool
		budget       int
		accept       bool
		alwaysFail   bool
		errorBody    string
		calls        int
		wantError    bool
	}{
		{name: "disabled", retryEnabled: true, budget: 3, calls: 1, wantError: true},
		{name: "retry_disabled", enabled: true, calls: 1, wantError: true},
		{name: "zero_budget", enabled: true, retryEnabled: true, calls: 1, wantError: true},
		{name: "valid_history", enabled: true, retryEnabled: true, budget: 3, accept: true, calls: 1},
		{name: "one_recovery_only", enabled: true, retryEnabled: true, budget: 5, alwaysFail: true, calls: 2, wantError: true},
		{name: "message_is_not_code", enabled: true, retryEnabled: true, budget: 3, errorBody: `{"error":{"message":"invalid_encrypted_content","code":"invalid_parameter"}}`, calls: 1, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executor := &reasoningRecoveryExecutor{acceptHistory: tt.accept, alwaysFail: tt.alwaysFail, errorBody: tt.errorBody}
			ctx, _, processor, _ := newReasoningRecoveryOrchestrator(t, executor)
			processor.SystemService.CompatibilityConfig.InvalidEncryptedContentRecovery = tt.enabled
			require.NoError(t, processor.SystemService.SetRetryPolicy(ctx, &biz.RetryPolicy{
				Enabled: tt.retryEnabled, MaxSingleChannelRetries: tt.budget, LoadBalancerStrategy: biz.LoadBalancerStrategyAdaptive,
			}))
			_, err := processor.Process(ctx, recoveryHTTPRequest(t, false))
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, executor.requests, tt.calls)
			require.True(t, gjson.GetBytes(executor.requests[0].Body, `input.#(type=="reasoning")`).Exists())
		})
	}
}

func TestReasoningRecovery_KeepsMappedModelAndStickyPolicy(t *testing.T) {
	for _, sticky := range []bool{false, true} {
		t.Run(fmt.Sprintf("sticky_%v", sticky), func(t *testing.T) {
			executor := &reasoningRecoveryExecutor{}
			ctx, _, processor, _ := newReasoningRecoveryOrchestrator(t, executor)
			candidate := processor.channelSelector.(*staticChannelSelector).candidates[0]
			candidate.TraceSticky = sticky
			candidate.Models = append(candidate.Models, biz.ChannelModelEntry{RequestModel: "gpt-4", ActualModel: "other-model"})
			_, err := processor.Process(ctx, recoveryHTTPRequest(t, false))
			if sticky {
				require.Error(t, err)
				require.Len(t, executor.requests, 1)
			} else {
				require.NoError(t, err)
				require.Len(t, executor.requests, 2)
			}
			for _, request := range executor.requests {
				require.Equal(t, "gpt-6-astra", gjson.GetBytes(request.Body, "model").String())
			}
		})
	}
}

func TestReasoningRecovery_ObeysRPMAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled_%v", canceled), func(t *testing.T) {
			executor := &reasoningRecoveryExecutor{}
			ctx, client, processor, selected := newReasoningRecoveryOrchestrator(t, executor)
			selected.Settings.RateLimit.RPM = lo.ToPtr(int64(1))
			if canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				executor.onCall = cancel
			}
			_, err := processor.Process(ctx, recoveryHTTPRequest(t, false))
			require.Error(t, err)
			require.Len(t, executor.requests, 1)
			require.Equal(t, int64(1), processor.rateLimitTracker.GetRequestCount(selected.ID))
			if !canceled {
				require.ErrorIs(t, err, ErrLocalRPMExhausted)
				executions, queryErr := client.RequestExecution.Query().All(ctx)
				require.NoError(t, queryErr)
				require.Len(t, executions, 2, "本地准入拒绝也需记录，但不额外触达上游")
			}
		})
	}
}

func TestReasoningRecovery_DoesNotReplayDeliveredStream(t *testing.T) {
	executor := &reasoningRecoveryExecutor{acceptHistory: true, afterContentError: true}
	ctx, client, processor, selected := newReasoningRecoveryOrchestrator(t, executor)
	result, err := processor.Process(ctx, recoveryHTTPRequest(t, true))
	require.NoError(t, err)
	require.NotNil(t, result.ChatCompletionStream)
	defer result.ChatCompletionStream.Close()
	sawContent, sawError := false, false
	for result.ChatCompletionStream.Next() {
		event := result.ChatCompletionStream.Current()
		sawContent = sawContent || event.Type == "response.output_text.delta"
		sawError = sawError || (event.Type == "error" && gjson.GetBytes(event.Data, "code").String() == "invalid_encrypted_content")
	}
	require.True(t, sawContent)
	require.True(t, sawError, "透传必须将协议错误交付客户端，而不是吞掉错误")
	require.NoError(t, result.ChatCompletionStream.Close())
	require.Len(t, executor.requests, 1)
	waitForRecoveryDrain(t, ctx, client, processor, selected)
}

func TestReasoningRecovery_AppliesAfterOverrides(t *testing.T) {
	executor := &reasoningRecoveryExecutor{}
	ctx, client, processor, selected := newReasoningRecoveryOrchestrator(t, executor)
	selected.Settings.BodyOverrideOperations = []objects.OverrideOperation{{
		Op: "array_append", Path: "input", Value: `{"type":"reasoning","encrypted_content":"gAAAA_override_fixture"}`,
	}}
	_, err := processor.Process(ctx, recoveryHTTPRequest(t, false))
	require.NoError(t, err)
	require.Len(t, executor.requests, 2)
	require.Len(t, gjson.GetBytes(executor.requests[0].Body, `input.#(type=="reasoning")#`).Array(), 2)
	require.False(t, gjson.GetBytes(executor.requests[1].Body, `input.#(type=="reasoning")`).Exists())
	executions, err := client.RequestExecution.Query().Order(ent.Asc(requestexecution.FieldID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, executions, 2)
	require.JSONEq(t, string(executor.requests[1].Body), string(executions[1].RequestBody))
}

func TestReasoningRecovery_DropsStaleJSONBodyWithoutMutatingCaller(t *testing.T) {
	original := &httpclient.Request{
		APIFormat: llm.APIFormatOpenAIResponse.String(), Body: []byte(recoveryRequestBody), JSONBody: []byte(recoveryRequestBody),
	}
	outbound := &PersistentOutboundTransformer{
		state: &PersistenceState{}, reasoningRecovery: reasoningRecovery{enabled: true, used: true},
	}
	filtered, err := applyReasoningRecovery(outbound).OnOutboundRawRequest(t.Context(), original)
	require.NoError(t, err)
	require.NotSame(t, original, filtered)
	require.Empty(t, filtered.JSONBody, "旧的日志正文不应覆盖恢复后的实际正文")
	require.Equal(t, recoveryRequestBody, string(original.Body))
	require.Equal(t, recoveryRequestBody, string(original.JSONBody))
	require.Same(t, filtered, outbound.state.RawProviderRequest)
}

func TestInvalidEncryptedContentError_StrictEnvelope(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"exact", 400, `{"error":{"code":"invalid_encrypted_content"}}`, true},
		{"success", 200, `{"error":{"code":"invalid_encrypted_content"}}`, false},
		{"server_error", 500, `{"error":{"code":"invalid_encrypted_content"}}`, false},
		{"text", 400, `invalid_encrypted_content`, false},
		{"message", 400, `{"error":{"message":"invalid_encrypted_content"}}`, false},
		{"wrong_code", 400, `{"error":{"code":"invalid_encrypted_content_extra"}}`, false},
		{"nested_output", 400, `{"output":{"error":{"code":"invalid_encrypted_content"}}}`, false},
		{"top_level_code", 400, `{"code":"invalid_encrypted_content"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isInvalidEncryptedContentError(&httpclient.Error{StatusCode: tt.status, Body: []byte(tt.body)}))
		})
	}
	require.False(t, isInvalidEncryptedContentError(nil))
}

func TestStripReasoningHistory_PreservesOpaqueData(t *testing.T) {
	body := []byte(`{"model":"target","input":[{"role":"user","content":[{"type":"input_file","file_id":"keep-file"}]},{"type":"reasoning","encrypted_content":"fixture"},{"type":"function_call","id":"keep-fc","call_id":"call-fn","arguments":"{\"n\":9007199254740993,\"html\":\"<tag>\"}"},{"type":"function_call_output","call_id":"call-fn","output":{"id":9007199254740993,"encrypted_content":"keep-data"}},{"type":"custom_tool_call","call_id":"call-custom","name":"check","input":"raw <input>"},{"type":"custom_tool_call_output","call_id":"call-custom","output":[{"type":"input_text","text":"opaque"}]},{"type":"future_tool","id":"keep-future","nested":{"type":"reasoning","encrypted_content":"keep-nested"}}],"max_output_tokens":9007199254740993,"prompt_cache_key":"keep-cache","reasoning":{"effort":"high","context":"all_turns"},"include":["reasoning.encrypted_content"]}`)
	original := bytes.Clone(body)
	filtered, count := stripReasoningHistory(body)
	require.Equal(t, 1, count)
	require.Equal(t, original, body)
	decode := func(raw []byte) map[string]any {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value map[string]any
		require.NoError(t, decoder.Decode(&value))
		return value
	}
	want := decode(body)
	items := want["input"].([]any)
	want["input"] = append(items[:1:1], items[2:]...)
	require.Equal(t, want, decode(filtered))
	require.Equal(t, "9007199254740993", gjson.GetBytes(filtered, "max_output_tokens").Raw)
	again, secondCount := stripReasoningHistory(filtered)
	require.Zero(t, secondCount)
	require.Equal(t, filtered, again)
}

func TestStripReasoningHistory_ProtectsStatefulAndMalformedInput(t *testing.T) {
	for _, extra := range []string{
		`"previous_response_id":"resp_fixture"`, `"conversation":"conv_fixture"`, `"conversation":{"id":"conv_fixture"}`,
	} {
		body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"fixture"},{"role":"user","content":"hello"}],` + extra + `}`)
		filtered, count := stripReasoningHistory(body)
		require.Zero(t, count)
		require.Equal(t, body, filtered)
	}
	for _, item := range []string{
		`{"type":"compaction","encrypted_content":"fixture"}`, `{"type":"compaction_summary"}`,
		`{"type":"item_reference","id":"fixture"}`, `{"id":"fixture"}`,
		`{"type":"future_state","encrypted_content":"fixture"}`, `null`, `7`,
	} {
		body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"fixture"},` + item + `,{"role":"user","content":"hello"}]}`)
		filtered, count := stripReasoningHistory(body)
		require.Zero(t, count)
		require.Equal(t, body, filtered)
	}
	for _, body := range []string{``, `null`, `[]`, `{`, `{"input":"hello"}`, `{"input":null}`, `{"input":[{"type":"reasoning"}]}`} {
		filtered, count := stripReasoningHistory([]byte(body))
		require.Zero(t, count)
		require.Equal(t, body, string(filtered))
	}
}

func TestReasoningRecovery_OnlyResponsesAndAttemptScoped(t *testing.T) {
	for _, format := range []llm.APIFormat{llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIChatCompletion, llm.APIFormatOpenAIResponseCompact} {
		r := reasoningRecovery{enabled: true, request: &httpclient.Request{APIFormat: format.String(), Body: []byte(recoveryRequestBody)}}
		r.observeError(&httpclient.Error{StatusCode: 400, Body: []byte(`{"error":{"code":"invalid_encrypted_content"}}`)})
		require.Equal(t, format == llm.APIFormatOpenAIResponse, r.pending)
		r.observeError(&httpclient.Error{StatusCode: 429})
		require.False(t, r.pending)
	}
}

func TestReasoningRecovery_StatefulRequestsRemainUnchanged(t *testing.T) {
	for _, kind := range []string{"compaction", "item_reference", "previous_response_id", "conversation"} {
		t.Run(kind, func(t *testing.T) {
			executor := &reasoningRecoveryExecutor{}
			ctx, _, processor, _ := newReasoningRecoveryOrchestrator(t, executor)
			request := recoveryHTTPRequest(t, false)
			var err error
			if kind == "compaction" || kind == "item_reference" {
				request.Body, err = sjson.SetRawBytes(request.Body, "input.-1", []byte(`{"type":"`+kind+`","id":"state_fixture","encrypted_content":"fixture"}`))
			} else {
				request.Body, err = sjson.SetBytes(request.Body, kind, "state_fixture")
			}
			require.NoError(t, err)
			original := bytes.Clone(request.Body)
			_, err = processor.Process(ctx, request)
			require.Error(t, err)
			require.Len(t, executor.requests, 1)
			require.True(t, gjson.GetBytes(executor.requests[0].Body, `input.#(type=="reasoning")`).Exists())
			require.Equal(t, original, request.Body)
		})
	}
}

func TestReasoningRecovery_StaysUsedAcrossChannelSwitch(t *testing.T) {
	first := &ChannelModelsCandidate{Channel: &biz.Channel{Channel: &ent.Channel{ID: 1}, Outbound: &mockTransformer{}}, Models: []biz.ChannelModelEntry{{ActualModel: "first-model"}}}
	second := &ChannelModelsCandidate{Channel: &biz.Channel{Channel: &ent.Channel{ID: 2}, Outbound: &mockTransformer{}}, Models: []biz.ChannelModelEntry{{ActualModel: "second-model"}}}
	outbound := &PersistentOutboundTransformer{
		state:             &PersistenceState{CurrentCandidate: first, ChannelModelsCandidates: []*ChannelModelsCandidate{first, second}},
		reasoningRecovery: reasoningRecovery{enabled: true, used: true},
	}
	require.NoError(t, outbound.NextChannel(t.Context()))
	require.True(t, outbound.reasoningRecovery.used)
	request := &httpclient.Request{APIFormat: llm.APIFormatOpenAIResponse.String(), Body: []byte(recoveryRequestBody)}
	filtered, err := applyReasoningRecovery(outbound).OnOutboundRawRequest(t.Context(), request)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(filtered.Body, `input.#(type=="reasoning")`).Exists())
	outbound.reasoningRecovery.observeError(&httpclient.Error{StatusCode: 400, Body: []byte(`{"error":{"code":"invalid_encrypted_content"}}`)})
	require.False(t, outbound.reasoningRecovery.pending)
}

func TestOpaqueResponses400Recovery_ChannelScopeAndOnceOnly(t *testing.T) {
	const opaque = `{"error":{"type":"invalid_request_error","message":"bad response status code 400 (request id: fixture-request)"}}`
	for _, tc := range []struct {
		name                                   string
		allow, alwaysFail, passThrough, stream bool
		body                                   string
		calls                                  int
		wantError                              bool
	}{
		{name: "默认关闭", body: opaque, calls: 1, wantError: true},
		{name: "显式启用非流式", allow: true, body: opaque, calls: 2},
		{name: "显式启用转换流式", allow: true, stream: true, body: opaque, calls: 2},
		{name: "显式启用透传流式", allow: true, passThrough: true, stream: true, body: opaque, calls: 2},
		{name: "只允许一次恢复", allow: true, alwaysFail: true, body: opaque, calls: 2, wantError: true},
		{name: "明确参数错误不恢复", allow: true, body: `{"error":{"type":"invalid_request_error","code":"invalid_parameter","message":"bad response status code 400 (request id: fixture-request)"}}`, calls: 1, wantError: true},
		{name: "其他泛化错误不恢复", allow: true, body: `{"error":{"type":"invalid_request_error","message":"invalid codex request"}}`, calls: 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &reasoningRecoveryExecutor{alwaysFail: tc.alwaysFail, errorBody: tc.body}
			ctx, client, processor, selected := newReasoningRecoveryOrchestrator(t, executor)
			selected.Settings.PassThroughBody = lo.ToPtr(tc.passThrough)
			if tc.allow {
				processor.SystemService.CompatibilityConfig.ResponsesOpaque400RecoveryChannels = []string{strconv.Itoa(selected.ID)}
			} else {
				processor.SystemService.CompatibilityConfig.ResponsesOpaque400RecoveryChannels = []string{"other-channel"}
			}
			require.NoError(t, processor.SystemService.SetRetryPolicy(ctx, &biz.RetryPolicy{Enabled: true, MaxSingleChannelRetries: 3, LoadBalancerStrategy: biz.LoadBalancerStrategyAdaptive}))
			request := recoveryHTTPRequest(t, tc.stream)
			original := bytes.Clone(request.Body)
			result, err := processor.Process(ctx, request)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if tc.stream {
					_, err = streams.All(result.ChatCompletionStream)
					require.NoError(t, err)
					require.NoError(t, result.ChatCompletionStream.Close())
				}
			}
			require.Len(t, executor.requests, tc.calls)
			require.Equal(t, original, request.Body)
			waitForRecoveryDrain(t, ctx, client, processor, selected)
			if tc.calls == 2 {
				require.True(t, gjson.GetBytes(executor.requests[0].Body, `input.#(type=="reasoning")`).Exists())
				require.False(t, gjson.GetBytes(executor.requests[1].Body, `input.#(type=="reasoning")`).Exists())
				for _, kind := range []string{"function_call", "function_call_output"} {
					path := `input.#(type=="` + kind + `")`
					require.JSONEq(t, gjson.GetBytes(executor.requests[0].Body, path).Raw, gjson.GetBytes(executor.requests[1].Body, path).Raw)
				}
			}
			executions, err := client.RequestExecution.Query().Order(ent.Asc(requestexecution.FieldID)).All(ctx)
			require.NoError(t, err)
			require.Len(t, executions, tc.calls)
			require.Equal(t, requestexecution.StatusFailed, executions[0].Status)
			if !tc.wantError {
				require.Equal(t, requestexecution.StatusCompleted, executions[len(executions)-1].Status)
			}
		})
	}
}

func TestNestedResponsesRateLimit_IsPreservedAndSkipsSameChannelRetry(t *testing.T) {
	executor := &reasoningRecoveryExecutor{acceptHistory: true, nestedRateLimit: true}
	ctx, client, processor, selected := newReasoningRecoveryOrchestrator(t, executor)
	selected.Settings.PassThroughBody = lo.ToPtr(false)
	require.NoError(t, processor.SystemService.SetRetryPolicy(ctx, &biz.RetryPolicy{Enabled: true, MaxSingleChannelRetries: 3, EmptyResponseDetection: true, LoadBalancerStrategy: biz.LoadBalancerStrategyAdaptive}))
	result, err := processor.Process(ctx, recoveryHTTPRequest(t, true))
	if err == nil {
		_, err = streams.All(result.ChatCompletionStream)
		_ = result.ChatCompletionStream.Close()
	}
	var upstream *llm.ResponseError
	require.True(t, errors.As(err, &upstream), "应保留上游协议错误: %v", err)
	require.Equal(t, http.StatusTooManyRequests, upstream.StatusCode)
	require.Equal(t, "rate_limit_exceeded", upstream.Detail.Code)
	require.Len(t, executor.requests, 1, "限流后应切换渠道，不在同渠道继续重试")
	waitForRecoveryDrain(t, ctx, client, processor, selected)
	executions, err := client.RequestExecution.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	require.Equal(t, requestexecution.StatusFailed, executions[0].Status)
	require.Contains(t, executions[0].ErrorMessage, "rate_limit_exceeded")
	require.Equal(t, http.StatusTooManyRequests, *executions[0].ResponseStatusCode)
}
