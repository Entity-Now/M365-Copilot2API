# HAR 逆向报告 10：工具协议状态与实现边界

## 已观测事实

- 本地共审计 16 个 HAR 文件，其中 M365 ChatHub 流量主要位于 `har1.har`、`har4.har` 及其重复命名副本。
- ChatHub 服务端流包含 `writeAtCursor` 增量、累计消息快照、`isLastUpdate`、SignalR `type:2` 最终结果和 `type:3` 完成帧。
- 审计到的 ChatHub 请求声明 `plugins:[{"Id":"BingWebSearch","Source":"BuiltIn"}]`。
- `TriggerPlugin` 出现在允许消息类型声明中，但本批接收帧没有提供完整的客户端工具调用生命周期证据。
- 本批 WebSocket 数据中没有观测到可用于稳定关联的 `call_id`、`callId` 或 `function_call_output`。
- HAR 中出现 MCP 和 Federated Connector 的发现或能力清单，但这不等同于 ChatHub 客户端工具调用协议。

## 实现推断

- 累计快照是正文权威来源，`writeAtCursor` 是与快照重叠的追加通道，不能简单拼接。
- 在没有稳定调用标识和工具结果续接证据时，把客户端工具伪装成 `Source:API` 插件或合成 `MCPServer` 插件会产生错误关联、重复调用和跨轮污染风险。
- 网关只能接受 ChatHub 实际返回的结构化工具事件，并校验工具名、参数 Schema 和调用 ID。自然语言、XML、围栏 JSON 或二次模型路由均不是已验证的工具协议。

## 未验证能力

- ChatHub 原生客户端工具的完整状态机。
- 参数跨帧增量及其顺序、重复和乱序语义。
- 上游调用 ID 与 OpenAI、Responses 或 Anthropic call ID 的稳定映射。
- 原生工具结果回传、取消、超时、重试和跨轮继续协议。
- 合成 MCPServer 插件负载的官方兼容性。

## 发布决策

- 仅接受结构化 ChatHub 工具事件。客户端声明工具但现有证据不足以建立完整上游工具生命周期时，明确返回 `unsupported_tool_protocol`。
- ChatHub 返回结构化工具事件，但工具名、参数或调用标识不符合客户端声明时，明确返回 `invalid_tool_call`。
- 不保留自然语言网关工具路由，不从普通文本、XML、围栏 JSON 或模型修复输出推断工具调用。
- 禁止静默启用未验证的 native/plugin 模式。
- 禁止向 ChatHub 合成 API-plugin 或 MCPServer 声明。
- 本地 `/v1/mcp` 能力与上游 ChatHub 插件协议分开处理。
- 后续仅在新的逐帧 HAR 证据覆盖调用开始、参数增量、结果回传和完成事件后，才考虑实现官方原生状态机。
