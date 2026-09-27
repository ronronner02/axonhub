package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
)

// 每个客户端请求独立持有，跨渠道切换也不重置 used。
type reasoningRecovery struct {
	enabled           bool
	used              bool
	pending           bool
	request           *httpclient.Request
	opaque400Channels []string
	allowOpaque400    bool
}

func (r *reasoningRecovery) observeError(rawErr *httpclient.Error) {
	r.pending = false
	if !r.enabled || r.used || r.request == nil ||
		r.request.APIFormat != llm.APIFormatOpenAIResponse.String() ||
		!(isInvalidEncryptedContentError(rawErr) || (r.allowOpaque400 && isOpaqueResponses400(rawErr))) {
		return
	}

	_, removed := stripReasoningHistory(r.request.Body)
	r.pending = removed > 0
}

func isInvalidEncryptedContentError(rawErr *httpclient.Error) bool {
	if rawErr == nil || rawErr.StatusCode != http.StatusBadRequest {
		return false
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(rawErr.Body, &payload) == nil && payload.Error.Code == "invalid_encrypted_content"
}

// 放在透传及 body override 之后、持久化和准入之前，避免恢复后再次混入旧密文。
func applyReasoningRecovery(outbound *PersistentOutboundTransformer) pipeline.Middleware {
	return pipeline.OnRawRequest("invalid-encrypted-content-recovery", func(_ context.Context, request *httpclient.Request) (*httpclient.Request, error) {
		recovery := &outbound.reasoningRecovery
		if !recovery.enabled {
			return request, nil
		}
		if recovery.used && request.APIFormat == llm.APIFormatOpenAIResponse.String() {
			body, removed := stripReasoningHistory(request.Body)
			if removed > 0 {
				copy := *request
				copy.Body = body
				// JSONBody 的持久化优先级更高，丢弃旧快照以记录实际出站正文。
				copy.JSONBody = nil
				request = &copy
			}
		}
		recovery.request = request
		outbound.state.RawProviderRequest = request
		return request, nil
	})
}

// 只移除独立的 reasoning 项。状态引用可能承载唯一历史，遇到它们时保留整份请求。
// RawMessage 避免把工具内容重新解码为 float64，保留大整数及未知字段。
func stripReasoningHistory(body []byte) ([]byte, int) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil ||
		hasHistoryReference(payload["previous_response_id"]) || hasHistoryReference(payload["conversation"]) {
		return body, 0
	}
	var items []json.RawMessage
	if json.Unmarshal(payload["input"], &items) != nil {
		return body, 0
	}

	kept := make([]json.RawMessage, 0, len(items))
	removed := 0
	for _, item := range items {
		var header struct {
			Type             string          `json:"type"`
			Role             string          `json:"role"`
			EncryptedContent json.RawMessage `json:"encrypted_content"`
		}
		if json.Unmarshal(item, &header) != nil {
			return body, 0
		}
		switch header.Type {
		case "reasoning":
			removed++
			continue
		case "compaction", "compaction_summary", "item_reference":
			return body, 0
		}
		if (header.Type == "" && header.Role == "") || hasHistoryReference(header.EncryptedContent) {
			return body, 0
		}
		kept = append(kept, item)
	}
	if removed == 0 || len(kept) == 0 {
		return body, 0
	}

	input, err := json.Marshal(kept)
	if err != nil {
		return body, 0
	}
	payload["input"] = input
	filtered, err := json.Marshal(payload)
	if err != nil {
		return body, 0
	}
	return filtered, removed
}

func hasHistoryReference(value json.RawMessage) bool {
	value = bytes.TrimSpace(value)
	return len(value) > 0 && !bytes.Equal(value, []byte("null")) && !bytes.Equal(value, []byte(`""`))
}

// 仅匹配已复现的泛化信封；明确参数错误、其他状态及消息不进入此分支。
func isOpaqueResponses400(rawErr *httpclient.Error) bool {
	if rawErr == nil || rawErr.StatusCode != http.StatusBadRequest {
		return false
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   string `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(rawErr.Body, &payload) != nil {
		return false
	}
	e := payload.Error
	const prefix = "bad response status code 400 (request id: "
	return e.Type == "invalid_request_error" && e.Code == "" && e.Param == "" &&
		strings.HasPrefix(e.Message, prefix) && strings.HasSuffix(e.Message, ")") && len(e.Message) > len(prefix)+1
}
