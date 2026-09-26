package orchestrator

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
)

const claudeCLIContinuationInstruction = "When working on a task, do not end your turn with a progress update or a statement of what you will inspect next. Continue using the available tools until the requested work is complete, then give a final answer."

// 只在目标渠道的 Claude Code 请求中追加指令；保留原有 system 块及缓存标记。
func guardClaudeCLIContinuation(outbound *PersistentOutboundTransformer, config biz.CompatibilityConfig) pipeline.Middleware {
	return pipeline.OnRawRequest("claude-cli-continuation", func(_ context.Context, request *httpclient.Request) (*httpclient.Request, error) {
		outbound.claudeCLIStreamContinuation = false
		channel := outbound.GetCurrentChannel()
		if request == nil || channel == nil || !config.AnthropicStreamRecoveryEnabledFor(channel.ID, channel.Name) ||
			request.APIFormat != llm.APIFormatAnthropicMessage.String() ||
			!isClaudeCLIRequest(outbound.state.LlmRequest) {
			return request, nil
		}

		outbound.claudeCLIStreamContinuation = true

		var body map[string]json.RawMessage
		if err := json.Unmarshal(request.Body, &body); err != nil || body == nil {
			return request, nil
		}

		var system []json.RawMessage
		if err := json.Unmarshal(body["system"], &system); err != nil || len(system) == 0 {
			return request, nil
		}

		instruction, err := json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text", Text: claudeCLIContinuationInstruction})
		if err != nil {
			return request, err
		}

		system = append(system, instruction)
		body["system"], err = json.Marshal(system)
		if err != nil {
			return request, err
		}

		encoded, err := json.Marshal(body)
		if err != nil {
			return request, err
		}

		updated := *request
		updated.Body = encoded
		updated.JSONBody = nil
		updated.Headers = request.Headers.Clone()
		updated.Headers.Del("Content-Length")
		outbound.state.RawProviderRequest = &updated

		return &updated, nil
	})
}

func isClaudeCLIRequest(request *llm.Request) bool {
	if request == nil || request.RawRequest == nil {
		return false
	}

	return strings.HasPrefix(strings.ToLower(request.RawRequest.Headers.Get("User-Agent")), "claude-cli/")
}
