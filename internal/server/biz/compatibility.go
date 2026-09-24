package biz

import (
	"strconv"
	"strings"
)

// CompatibilityConfig 只在启动时加载，不改变数据库中的重试策略。
type CompatibilityConfig struct {
	InvalidEncryptedContentRecovery bool     `conf:"invalid_encrypted_content_recovery" yaml:"invalid_encrypted_content_recovery" json:"invalid_encrypted_content_recovery"`
	AnthropicStreamRecoveryChannels []string `conf:"anthropic_stream_recovery_channels" yaml:"anthropic_stream_recovery_channels" json:"anthropic_stream_recovery_channels"`
}

// AnthropicStreamRecoveryEnabledFor 判断指定渠道名称或数字 ID 是否显式启用了兼容修复。
func (c CompatibilityConfig) AnthropicStreamRecoveryEnabledFor(channelID int, channelName string) bool {
	id := strconv.Itoa(channelID)
	name := strings.TrimSpace(channelName)

	for _, selector := range c.AnthropicStreamRecoveryChannels {
		selector = strings.TrimSpace(selector)
		if selector == id || (name != "" && strings.EqualFold(selector, name)) {
			return true
		}
	}

	return false
}
