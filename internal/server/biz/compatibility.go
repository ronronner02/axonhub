package biz

import (
	"strconv"
	"strings"
)

// CompatibilityConfig 只在启动时加载，不改变数据库中的重试策略。
type CompatibilityConfig struct {
	InvalidEncryptedContentRecovery    bool     `conf:"invalid_encrypted_content_recovery" yaml:"invalid_encrypted_content_recovery" json:"invalid_encrypted_content_recovery"`
	AnthropicStreamRecoveryChannels    []string `conf:"anthropic_stream_recovery_channels" yaml:"anthropic_stream_recovery_channels" json:"anthropic_stream_recovery_channels"`
	ResponsesOpaque400RecoveryChannels []string `conf:"responses_opaque_400_recovery_channels" yaml:"responses_opaque_400_recovery_channels" json:"responses_opaque_400_recovery_channels"`
}

// AnthropicStreamRecoveryEnabledFor 判断指定渠道名称或数字 ID 是否显式启用了兼容修复。
func (c CompatibilityConfig) AnthropicStreamRecoveryEnabledFor(channelID int, channelName string) bool {
	return matchesCompatibilityChannel(c.AnthropicStreamRecoveryChannels, channelID, channelName)
}

// 泛化 400 缺少具体校验原因，必须按渠道显式开启，不扩展全局恢复范围。
func (c CompatibilityConfig) ResponsesOpaque400RecoveryEnabledFor(channelID int, channelName string) bool {
	return matchesCompatibilityChannel(c.ResponsesOpaque400RecoveryChannels, channelID, channelName)
}

func matchesCompatibilityChannel(selectors []string, channelID int, channelName string) bool {
	id := strconv.Itoa(channelID)
	name := strings.TrimSpace(channelName)

	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if selector == id || (name != "" && strings.EqualFold(selector, name)) {
			return true
		}
	}

	return false
}
