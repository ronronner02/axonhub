---
description: "Workflow to add a new AI provider channel"
---

# Adding a New AI Provider Channel

## 1. Backend: Ent Schema & Code Generation

1. **Extend the channel type enum** — add the provider key to `field.Enum("type")` in `internal/ent/schema/channel.go:37-92`.
2. **Regenerate** — run `make generate` to regenerate Ent artifacts and GraphQL types after the schema change.
3. **Map default endpoints** — add an entry to `defaultEndpointsForChannelType` in `internal/server/biz/channel_endpoint.go:90-156`.
   - If the channel uses a new API format, also add it to `SupportedAPIFormats` in the same file.
4. **Wire the outbound transformer** — add a `case` to the switch in `ChannelService.buildChannelWithTransformer` (`internal/server/biz/channel_llm.go:348-951`).
   - If the channel uses a credential variant not covered by the `default` case, add credential validation above the switch (lines 320-343).
   - If adding a new transformer package under `llm/transformer/`, import it and build the config with `getAPIKeyProvider(ch)`.

## 2. Frontend: Schema, Config & i18n

1. **Zod schema** — add the new type value to `channelTypeSchema` enum in `frontend/src/features/channels/data/schema.ts:48-102`.
   - If a new API format, add to `apiFormatSchema` and `configurableChannelEndpointApiFormats` in the same file.
2. **Channel config** — add a `CHANNEL_CONFIGS` entry in `frontend/src/features/channels/data/config_channels.ts` (icon, baseURL, defaultModels, apiFormat, color).
3. **Provider config** — add a `PROVIDER_CONFIGS` entry in `frontend/src/features/channels/data/config_providers.ts` if a new provider group, or add the new channel type to an existing provider's `channelTypes` array.
4. **Internationalization** — add/update relevant keys in `frontend/src/locales/en.json` and `frontend/src/locales/zh.json`.

## 3. Finalize

- Run `make generate` again to pick up any generated code changes that may have been introduced by the new transformer or endpoint wiring.
- Verify the build compiles (`make build-backend`).

## 4. 生产环境隐私脱敏接入

在 AxonHub 中新增渠道后，系统不会自动把它接到 privacy-redaction sidecar。
新的第三方渠道在启用前必须显式完成以下步骤：

1. 将上游精确主机名加入服务器部署环境的 `REDACT_ALLOWED_HOSTS`。不得用无关
   通配范围替代，也不得关闭主机白名单检查。
2. 针对精确渠道 ID，以 dry-run 运行
   `deploy/privacy-redaction/scripts/redact-channels.sh`。普通安全封套应为
   `http://redact:8787/$<upstream-url>`。
3. 只有上游协议存在已记录的兼容要求时，才使用 `PSIBEG$` 等降级检测标志位。
   必须用 `--only` 限定渠道、通过脚本的显式确认门禁，并在
   `deploy/privacy-redaction/OPERATIONS-LOG.md` 记录原因。
4. 审阅 dry-run 后再执行渠道改写。不得为了让 provider 测试通过而把第三方渠道
   以明文直连方式启用。
5. 运行 `deploy/privacy-redaction/scripts/check-trust-boundary.sh`。上线门槛是：
   违规数为 0，且允许主机集合与当前启用渠道推导出的集合完全一致。
6. 发送一条有界的合成请求并验证双向行为：上游不得收到敏感原值，客户端必须
   收到还原后的原值。容器 healthy 或 `/health` 200 不是 provider generation
   验收结果。
7. 在 `OPERATIONS-LOG.md` 中记录渠道 ID、上游主机、封套标志位、验证证据、
   回滚命令和操作人；适用时同步填写 `ACCEPTANCE-RECORD.md`。

当前脚本是审计与改写工具，不是自动监听器。因此，每个新建或新启用的第三方渠道
都必须在投入生产前执行本流程。
