# AI Bridge

> AI providers, models, and autonomous agents — exposed as a first-class Beeper chat network.

`ai-bridge` is a [mautrix `bridgev2`](https://docs.mau.fi/bridges/go/) network connector that makes "talking to an AI" indistinguishable from talking to a person on Beeper. Each AI model is a contact. Each conversation is a room. Each response streams in live, carries its reasoning, tool calls, citations, and generated images, and is fully resumable across restarts.

There are three ways to engage with it, and this README covers all three:

- **Run it** — deploy the bridge against a homeserver and chat with models inside Beeper. Start at [Quickstart](#quickstart) and [Configuration](#configuration-reference).
- **Build a client** — render or drive AI chats from a Matrix/Beeper client (the Beeper app, a web UI, a bot). [Part 1](#part-1--building-clients) documents the exact wire protocol you consume and the events you must handle.
- **Extend it** — add a provider, model, tool, or agent behavior by hacking on this codebase. [Part 2](#part-2--extending-the-bridge) maps the seams and the conventions to follow.

---

## Table of contents

- [The core idea](#the-core-idea)
- [Architecture at a glance](#architecture-at-a-glance)
- [Quickstart](#quickstart)
- [Mental model: how a message becomes an answer](#mental-model-how-a-message-becomes-an-answer)
- [Part 1 — Building clients](#part-1--building-clients)
  - [The `com.beeper.ai` envelope](#the-combeeperai-envelope)
  - [Anchor → stream → final lifecycle](#anchor--stream--final-lifecycle)
  - [The AG-UI event protocol](#the-ag-ui-event-protocol)
  - [`UIMessage`: the render model](#uimessage-the-render-model)
  - [Final payload & the 64 KiB problem](#final-payload--the-64-kib-problem)
  - [Approvals (human-in-the-loop)](#approvals-human-in-the-loop)
  - [Room capabilities you must respect](#room-capabilities-you-must-respect)
  - [Slash & bridge commands](#slash--bridge-commands)
  - [Generated media](#generated-media)
- [Part 2 — Extending the bridge](#part-2--extending-the-bridge)
  - [Providers vs APIs vs models](#providers-vs-apis-vs-models)
  - [The streaming interface](#the-streaming-interface)
  - [Adding a provider](#adding-a-provider)
  - [The model catalog](#the-model-catalog)
  - [Image generation](#image-generation)
  - [The agent runtime](#the-agent-runtime)
  - [The harness (production agent)](#the-harness-production-agent)
  - [Built-in chat tools & adding your own](#built-in-chat-tools--adding-your-own)
  - [Sessions: the branching conversation tree](#sessions-the-branching-conversation-tree)
  - [Compaction](#compaction)
  - [The remote proxy backend](#the-remote-proxy-backend)
- [Login & provider configuration](#login--provider-configuration)
- [Configuration reference](#configuration-reference)
- [ID scheme & persistence](#id-scheme--persistence)
- [Testing with the faux provider](#testing-with-the-faux-provider)
- [Quirks & gotchas (read this)](#quirks--gotchas-read-this)
- [Package map](#package-map)

---

## The core idea

A traditional chat bridge maps an external network (WhatsApp, Signal) onto Matrix. `ai-bridge` does the same thing — except the "network" is **the universe of AI models**.

- A **provider/model pair** (e.g. `openai/gpt-5`, `anthropic/claude-...`) is a **contact / ghost** you can start a DM with.
- That **DM is a room**, and the room is bound to one persistent **session** (a branching conversation tree).
- Sending a message **runs the model**. The reply **streams back** as a live-editing Matrix message carrying a rich structured payload.
- The model can **use tools** (web search, URL fetch, session introspection, plus provider-native tools like image generation), **reason**, **generate images**, and **cite sources** — all surfaced in the same payload.
- Everything is **persisted and resumable**: if the bridge restarts mid-response, the run is re-attached and finished.

Because it is a real `bridgev2` connector, it inherits Beeper's identity, provisioning, direct-media, and room-state machinery for free. Clients that already speak Matrix/Beeper get AI chats with zero new transport.

---

## Architecture at a glance

```
                         ┌──────────────────────────────────────────────┐
   Matrix / Beeper       │                pkg/connector                  │
   client  ◀────────────▶│   bridgev2.NetworkConnector + Client          │
   (renders com.beeper.ai)│   rooms↔sessions, slash cmds, login, routes  │
                         └───────┬───────────────────────┬──────────────┘
                                 │                        │
                   inbound msg → │                        │ ← streamed reply
                                 ▼                        │
                   ┌─────────────────────┐    ┌───────────┴───────────┐
                   │     pkg/msgconv     │    │     pkg/ai-stream     │
                   │ Matrix ⇄ AI message │    │ run → AG-UI events →  │
                   │     conversion      │    │  anchor/stream/final  │
                   └─────────┬───────────┘    └───────────┬───────────┘
                             │                            │ emits agui.Event
                             ▼                            ▼
                   ┌──────────────────────────────────────────────────┐
                   │           pkg/agent  +  pkg/agent/harness         │
                   │   autonomous tool-loop, sessions, compaction      │
                   │       pkg/chattools (web_search, fetch, …)        │
                   └────────────────────────┬─────────────────────────┘
                                            │ StreamFn
                                            ▼
                   ┌──────────────────────────────────────────────────┐
                   │                      pkg/ai                       │
                   │   provider/API registry · model catalog · stream  │
                   │     pkg/ai/providers (OpenAI, Google, …)          │
                   └──────────────────────────────────────────────────┘

   Foundations:  pkg/aiid (IDs/metadata) · pkg/aidb (bridge DB, resume) ·
                 pkg/agent/harness/session (per-conversation SQLite tree) ·
                 pkg/ag-ui (the wire event protocol)
```

The dependency direction is strict: `pkg/ai` never imports `pkg/ai/providers` (providers register into `ai` via a side-effect `init()`), and `ai-stream`/`ag-ui` are provider- and Matrix-agnostic at their core — the Matrix coupling lives in the connector and `ai-stream/matrix`.

---

## Quickstart

The build uses the `goolm` tag (pure-Go olm, no C dependency).

```sh
./build.sh          # → ./ai  (go build -tags=goolm ./cmd/ai)
./run.sh            # go run the bridge
./test.sh           # go test -tags=goolm ./...  (or pass packages)
```

The binary is a standard mautrix bridge (`cmd/ai/main.go`). It registers the AI connector and **blank-imports `pkg/ai/providers`** to populate the provider registry — without that import, `ai.Stream` panics. Generate a config the usual mautrix way; the AI-specific block is small (see [Configuration reference](#configuration-reference)).

Requirements:

- The Matrix connector must implement `MatrixConnectorWithBeeperStreams` (the bridge refuses to start otherwise — `connector.go:53`).
- For per-room settings (model/prompt/tools) the connector must implement `MatrixConnectorWithArbitraryRoomState`; without it those features silently disappear from capabilities.
- For HTTP provider provisioning, it must implement `MatrixConnectorWithProvisioning`.

---

## Mental model: how a message becomes an answer

1. **Inbound.** A user sends a message to an AI room. `pkg/msgconv/from_matrix.go` turns it into a `MatrixPrompt` (text + image/audio/text-file attachments, plus reply context). Image/audio attachments are rejected if the room's model can't accept that modality.
2. **Command check.** If the body starts with `/` and matches a known command, it's handled as a [slash command](#slash--bridge-commands) instead of a prompt. Unknown `/foo` is *not* an error — it's sent to the model verbatim.
3. **Session.** The room's `PortalMetadata.SessionID` resolves the conversation's session (lazily created on first message). The model + reasoning + extra system prompt come from room state.
4. **Run.** A `harness.AgentHarness` builds the LLM context from the session tree and starts an autonomous **agent loop**: stream a response → execute any tool calls → feed results back → repeat until the model stops calling tools.
5. **Stream out.** Every provider event is folded into an `ai-stream.Run`, which emits a validated sequence of **AG-UI events** and projects them into Matrix payloads: one **anchor** placeholder, many **stream** carriers, one **final** edit.
6. **Persist & resume.** The run is recorded in the bridge DB as an *active stream* so a restart can finish it. Each assistant turn is appended to the session tree. On completion the active-stream record is deleted.
7. **Compact.** After each assistant message, autocompaction checks whether the context is near the window limit and summarizes older history if so.

---

# Part 1 — Building clients

A client is anything that **renders** an AI chat or **drives** it (responds to approvals, sends prompts). You consume Matrix messages as usual; the AI-specific richness lives in one event-content key.

## The `com.beeper.ai` envelope

Every AI message carries a `com.beeper.ai` object (`BeeperAIKey`) in its event content. Schema `com.beeper.ai.v1`, protocol `"ag-ui"`. The shape is defined by `BeeperAI` in `pkg/ai-stream/run.go`:

```jsonc
{
  "schema": "com.beeper.ai.v1",
  "protocol": "ag-ui",
  "kind": "anchor" | "stream" | "final",
  "threadId": "...", "runId": "...", "messageId": "msg-<runId>",
  "agent": { "id": "...", "displayName": "..." },
  "model": "provider/model",
  "message": { /* UIMessage — present on anchor & final */ },
  "events": [ /* sequenced AG-UI events — present on stream kind; final kind includes the terminal RUN_FINISHED/RUN_ERROR event */ ],
  "data": { /* arbitrary com.beeper.data values */ },
  "final": { "delivery": "inline|attachment", "textComplete": true,
             "partsComplete": true, "partsRef": {...} }
}
```

You can render at three levels of fidelity:

- **Lowest effort:** read the Matrix `body` (the bridge always sets sensible plaintext/HTML) and ignore `com.beeper.ai`. You get a static final answer with no streaming or tool detail.
- **Medium:** read `message` (a [`UIMessage`](#uimessage-the-render-model)) from the **anchor** (for the live preview) and the **final** edit (for the consolidated result). Skip the incremental carriers.
- **Full:** replay the `envelopes` from every **stream** carrier to animate text/reasoning/tool-call deltas in real time.

## Anchor → stream → final lifecycle

A single logical assistant message moves through three Matrix events:

| Kind | Matrix mechanism | Purpose |
|------|------------------|---------|
| **anchor** | the original posted message | placeholder; `message` = initial preview text |
| **stream** | events *related to* the anchor (`m.reference`) | incremental carriers; each holds a batch of sequenced AG-UI `envelopes` |
| **final** | an **edit** of the anchor | consolidated result; carries `com.beeper.dont_render_edited: true` |

Stream carriers are published incrementally — only events appended since the last publish are packed, with globally monotonic sequence numbers (`Envelope.Seq`) and deterministic transaction IDs (`ai_stream_<runID>_<seq>`) for idempotency. A client that understands AG-UI reconstructs the message by ordering envelopes by `Seq`; a client that doesn't simply shows the final edit.

## The AG-UI event protocol

[`pkg/ag-ui`](pkg/ag-ui) is a Go port of the AG-UI streaming spec — the vocabulary inside `envelopes`. An `agui.Event` is an open `map[string]any` (forward-compatible: unknown fields survive round-trips), with a `type` discriminator. The full catalog (`events.go`):

- **Lifecycle:** `RUN_STARTED`, `RUN_FINISHED`, `RUN_ERROR`
- **Text:** `TEXT_MESSAGE_START` / `_CONTENT` / `_END` / `_CHUNK`
- **Reasoning:** `REASONING_START` / `_END`, `REASONING_MESSAGE_START` / `_CONTENT` / `_END` / `_CHUNK`, `REASONING_ENCRYPTED_VALUE`
- **Tools:** `TOOL_CALL_START` / `_ARGS` / `_END` / `_CHUNK` / `_RESULT`
- **Steps:** `STEP_STARTED`, `STEP_FINISHED`
- **State:** `STATE_SNAPSHOT`, `STATE_DELTA`, `MESSAGES_SNAPSHOT`
- **Activity:** `ACTIVITY_SNAPSHOT`, `ACTIVITY_DELTA`
- **Escape hatches:** `RAW`, `CUSTOM`

The protocol is **validated** (`validation.go`): exactly one `RUN_STARTED`; nothing after a terminal `RUN_FINISHED`/`RUN_ERROR`; content/end events must follow a matching start; no sequence may be left open at termination; `TOOL_CALL_END` must not carry a result (use `TOOL_CALL_RESULT`). Tool-call states are `awaiting-input` → `input-streaming` → `input-complete`; tool-result states are `streaming` / `complete` / `error`. `CUSTOM` events named `com.beeper.source` / `com.beeper.document` / `com.beeper.file` / `com.beeper.data` carry citations, artifacts, and arbitrary data.

`pkg/ag-ui/capabilities.go` defines `AgentCapabilities` — the agent self-description handshake (transport, tools, reasoning, multimodal in/out, human-in-the-loop, execution limits) that lets clients negotiate features.

## `UIMessage`: the render model

The `message` field is a `UIMessage` (`pkg/ai-stream/ui_message.go`) — an ordered list of typed **parts**:

```jsonc
{ "id": "...", "role": "assistant",
  "parts": [
    { "type": "thinking", "content": "...", "state": "done" },
    { "type": "text", "content": "...", "state": "streaming|done" },
    { "type": "tool-call", "toolCallId": "...", "name": "web_search",
      "input": {...}, "output": {...}, "state": "input-complete|approval-requested|..." },
    { "type": "source-url", ... }, { "type": "file", ... },
    { "type": "data-com-beeper-data", ... }
  ] }
```

Part types: `text`, `thinking` (also used for step markers), `tool-call`, `source-url`, `source-document`, `file`, `data-com-beeper-data`. Notes for renderers:

- A text/reasoning block that re-opens after a tool call is split into `…-segment-N` parts — render them in order, don't merge.
- On termination, any tool-call part with no output gets a synthetic error output, so you never see a tool stuck "in progress."
- `tool-call` states include `approval-requested` and `approval-responded` (see below).

## Final payload & the 64 KiB problem

Matrix caps event content at 64 KiB. A rich `UIMessage` with full tool inputs/outputs and citations can exceed that. The `final` field tells you how the bridge handled it (`pkg/ai-stream/final_payload.go`):

- `delivery: "inline"` — `message.parts` is complete and authoritative.
- `delivery: "attachment"` — `message.parts` is **empty**; `final.partsRef` points to an uploaded JSON blob (`application/vnd.beeper.ai.final-parts+json`) with a `sha256`, `byteSize`, and `url`/`file`. Download it, verify the hash, and render its `message`.
- `textComplete` — whether the rendered body text was truncated (`[See more on supported clients]` is appended when so).
- `partsComplete` — whether `message.parts` is the full set.

**A correct client must handle both delivery modes.**

## Approvals (human-in-the-loop)

Some tool calls require explicit user approval before executing (`pkg/ai-stream/approval.go`). When that happens:

- The run **interrupts** (`RUN_FINISHED` with `outcome: interrupt`, reason `tool_call`), and the relevant `tool-call` part flips to `approval-requested` with the proposed `input`.
- A dedicated approval message is posted (relation type `com.beeper.ai.approval`, key under `com.beeper.ai.approval`, schema `com.beeper.ai.approval.v1`) listing the choices. Defaults: **Allow once** (`approve`), **Always allow** (`always_approve`), **Deny** (`deny`, danger style).
- The user responds with `/approve <approval-id> <approve|always|deny>`. The bridge resolves the choice, emits a `TOOL_CALL_RESULT`, and resumes the run.
- Approvals are queued (one active at a time) and time out to a denied result if unanswered.

The response schema (if you drive approvals programmatically rather than via slash commands) is `{ approved: bool, always?: bool, reason?: string, editedArgs?: {...} }`.

## Room capabilities you must respect

Per-room capabilities (`pkg/connector/capabilities.go`) are tailored to the room's model:

- **Formatting:** Matrix rich text is fully supported for user messages; the bridge preserves `formatted_body` and advertises full support for the Matrix formatting feature set.
- **Attachments:** text-like files always (≤512 KB); images only if the model has `image` input (PNG/JPEG/WebP, ≤20 MB); audio + voice only if `audio` input (WAV/MP3/MPEG, ≤25 MB). Formatted captions are preserved.
- **Location messages:** fully supported as prompt text.
- `MaxTextLength: 20000`. **Reply:** full. **Edit: rejected. Reaction: unsupported.** **Delete:** full.
- Disappearing messages supported; typing notifications on; read receipts off.

Don't build a client that depends on editing AI messages or reacting to them as a general affordance.

## Slash & bridge commands

Clients can offer these as UI affordances; they're just messages. See the [command table](#slash--bridge-commands) below.

## Generated media

AI-generated images and inline media are served through the bridge's **direct-media** path (`pkg/connector/directmedia.go`). A `MediaID` is either a sanitized human form or a self-describing base64-JSON blob (`ai:<base64>`) that encodes the session/entry/content-index needed to stream the bytes back (or a URL redirect). Clients just download the MXC URI as normal.

---

# Part 2 — Extending the bridge

Everything here is done by editing this Go codebase and recompiling — there is no plugin system or external service interface. "Extending" means widening what the AI *can do*: wiring in a new provider or wire protocol, adding a model, changing agent behavior, or writing a tool.

## Providers vs APIs vs models

These three concepts (`pkg/ai/types.go`) are distinct and often confused:

| Concept | Type | What it is | Example |
|---------|------|------------|---------|
| **Provider** | `ai.Provider` | *who you talk to / who bills you* — the vendor or gateway. Determines API-key env var, base URL, compat quirks. | `openai`, `anthropic`, `google`, `openrouter`, `groq`, `xai`, … (~35) |
| **API** | `ai.Api` | *the wire protocol* — request/response shape. **This is the dispatch key.** | `openai-responses`, `openai-completions`, `anthropic-messages`, `google-generative-ai`, `google-vertex`, … |
| **Model** | `ai.Model` | a concrete model tying an ID to a provider, API, base URL, capabilities, and pricing. | `gpt-5`, `claude-...`, `gemini-...` |

The key relationship: **many providers share one API.** Groq, xAI, DeepSeek, Together, OpenRouter, etc. all speak `openai-completions`. Dispatch happens on `model.API`, then provider-specific behavior is layered via the `Provider` field and a free-form `Compat map[string]any`.

`ai.Model` carries everything callers need: `Reasoning`/`ThinkingLevelMap`/`DefaultThinkingLevel`, `Input`/`Output` modalities, `Cost` (USD/1M tokens), `ContextWindow`, `MaxTokens`, `BuiltInTools`, `Headers`, and `Compat`.

## The streaming interface

One uniform interface over every protocol (`pkg/ai/stream.go`):

```go
ai.Stream(ctx, model, ai.Context{SystemPrompt, Messages, Tools}, StreamOptions) *AssistantMessageEventStream
ai.StreamSimple(ctx, model, ctx, SimpleStreamOptions) *AssistantMessageEventStream   // adds Reasoning + ThinkingBudgets
ai.Complete / ai.CompleteSimple(...)                                                  // block, return final Message
```

The returned `*AssistantMessageEventStream` is a buffered channel of `AssistantMessageEvent`. Event types: `start`, `text_start`/`_delta`/`_end`, `thinking_start`/`_delta`/`_end`, `toolcall_start`/`_delta`/`_end`, `raw` (Responses API), terminal `done` (with final `Message`), and `error`. Consume with `for ev := range s.Events()` or block with `s.Result()`. `StopReason` is one of `stop | length | toolUse | error | aborted` (providers upgrade `stop`→`toolUse` when a tool call is present).

`StreamOptions` exposes two interception hooks every provider honors:

- `OnPayload(payload, model) (newBody, replace, err)` — inspect/rewrite the outgoing request body before send.
- `OnResponse(ProviderResponse, model) error` — observe HTTP status/headers.

Content is the universal `ContentBlock` (`text` / `thinking` / `toolCall` / `image` / `audio`), with provenance and signatures for round-tripping reasoning across turns.

## Adding a provider

**Same protocol, new vendor** — usually *no code*:

1. Add a model entry in ai-services, or configure a custom provider model with the right `API`, `Provider`, `BaseURL`.
2. Map the provider to its API-key env var(s) in `pkg/ai/env_api_keys.go`.
3. Add any `Compat` overrides (most base-URL patterns are auto-detected by `detectOpenAICompletionsCompat`).

**A new wire protocol:**

1. Add an `Api` constant in `pkg/ai/types.go`.
2. Implement `StreamSimpleX(ctx, model, ai.Context, SimpleStreamOptions) *ai.AssistantMessageEventStream` following the goroutine + state-machine template the existing providers use (create stream, spawn goroutine, push `start`, drive a stream-state machine that mutates the accumulating message and pushes incremental events, push `done`/`error`).
3. Register it: add to `RegisterBuiltInAPIProviders` (`pkg/ai/providers/register_builtins.go`) or call `ai.RegisterAPIProviderWithSource` from your own package's `init()`. The registry auto-derives missing `Stream`/`CompleteSimple` from whatever you provide, and wraps everything with an API-mismatch guard.

Remember the **side-effect import**: consumers must `import _ ".../pkg/ai/providers"` (or your package) to populate the registry.

Provider-specific behaviors worth knowing live in `pkg/ai/providers`: OpenAI *Completions* vs *Responses* are very different protocols; Google configures thinking per model family (discrete level vs numeric budget); cross-cutting `transformMessages` downgrades unsupported modalities, strips/recovers reasoning across models, and injects synthetic tool results for dangling tool calls.

## The model catalog

The model catalog is owned by ai-services. The bridge loads `/models?feature=bridge:ai`, applies each model's runtime metadata, and fails provider resolution if ai-services does not return a catalog. There is no bridge-generated fallback catalog.

Custom providers use the same ai-services catalog for model metadata. The bridge filters that catalog to the supported provider runtime (`openai`, `openrouter`, `anthropic`, or `google-vertex`) and then uses the user's configured base URL and API key for execution. Arbitrary model IDs and generic OpenAI-compatible providers are not accepted.

Reasoning levels form a ladder `off < minimal < low < medium < high < xhigh`; `ClampThinkingLevel` snaps a request to the nearest supported level from the model metadata the bridge was given.

## Image generation

Image generation is a **separate path** (`pkg/ai/images.go`, `images_*.go`): `ai.GenerateImages(ctx, ImagesModel, ImagesContext, ImagesOptions) AssistantImages` (synchronous, no streaming). Image model metadata must come from ai-services or explicit provider configuration; the bridge does not keep a generated image catalog. The built-in implementation supports OpenRouter; blank-import `pkg/ai/providers/images` to enable it. Models can also expose **provider-native** `image_generation` as a built-in tool (see [chat tools](#built-in-chat-tools--adding-your-own)).

## The agent runtime

[`pkg/agent`](pkg/agent) is the autonomous tool-using loop. A **run** = one `RunAgentLoop` call; a **turn** = one assistant response plus execution of its tool calls. The loop streams a response, executes tool calls (parallel by default, sequential if any tool requests it), feeds results back, and repeats **while the model keeps calling tools or messages are queued**.

```go
messages, err := agent.RunAgentLoop(ctx, prompts, agent.AgentContext{SystemPrompt, Messages, Tools},
    agent.AgentLoopConfig{Model: m, GetAPIKey: getKey, /* hooks… */}, emitSink, ai.StreamSimple)
```

Tools are `agent.AgentTool[any]` — an `ai.Tool` (name + JSON schema) plus an `Execute` closure. Arguments are schema-validated before `Execute` runs; returning an error becomes an error tool-result the model sees (it never crashes the loop). A tool can set `Terminate` to end the run (but only if **every** tool in the batch terminates). Rich hooks let you intercept everything: `BeforeToolCall` (can block), `AfterToolCall` (can rewrite results), `PrepareNextTurn` (swap context/model/thinking level), `ShouldStopAfterTurn`, `TransformContext`.

> **Gotcha:** there is **no built-in turn/iteration cap.** A model that loops on tool calls runs forever unless you bound it via `ShouldStopAfterTurn`, a context deadline, or `Terminate`.

The stateful `agent.Agent` wrapper adds queueing (`Steer`/`FollowUp`), subscriptions, and abort — but assumes a single driver goroutine (its state is not internally synchronized).

## The harness (production agent)

[`pkg/agent/harness`](pkg/agent/harness) is the production façade and what the connector actually uses. On top of the loop it adds:

- **Persistent sessions** — a branching conversation tree (below).
- **System-prompt resolution**, **model / thinking-level switching** (persisted as tree entries).
- **Three queues** (`Steer`, `FollowUp`, `NextTurn`) and a phase state machine (`idle`/`turn`/`compaction`/`branch_summary`).
- **A pub/sub + hook system** — `Subscribe` for observers, `On(eventType, handler)` for behavior-modifying hooks (`before_agent_start`, `context`, `tool_call`, `tool_result`, `before_provider_request`, `before_provider_payload`, `after_provider_response`, `session_before_compact`, `session_before_tree`).
- **Auth/header injection** and **compaction/summarization**.

```go
h, _ := harness.NewAgentHarness(harness.AgentHarnessOptions{
    Session: session, Model: m, ThinkingLevel: agent.ThinkingLevelMedium,
    SystemPromptFunc: buildPrompt, Tools: tools, ActiveToolNames: []string{"web_search"},
    StreamFn: ai.StreamSimple, GetAPIKeyAndHeaders: resolveAuth,
    CompactionSettings: cfg.Compaction.Settings(),
})
result, err := h.PromptWithResult(ctx, "do the thing")
```

Errors are typed with codes (`pkg/agent/harness/public_errors.go`): `CompactionError`, `BranchSummaryError`, `AgentHarnessError` (`busy`, `invalid_state`, `auth`, `hook`, …) — switch on the code for user-facing messages.

## Built-in chat tools & adding your own

[`pkg/chattools`](pkg/chattools) provides the tools the AI gets out of the box:

| Tool | Purpose | Notes |
|------|---------|-------|
| `get_session` | Live chat metadata (current time/timezone, model, reasoning, search/fetch modes, attachments) | read-only; recomputes time per call |
| `fetch` | Fetch a full HTTP/HTTPS URL → readable text + metadata | Beeper mode: direct fetch (≤2 MiB, ≤20 000 chars) or AI-services `/tools/fetch` extraction with fallback; native mode: provider URL-context/fetch tool when available |
| `web_search` | Web search | Exa-backed Beeper search, enabled when room search mode is `beeper`; returns concise URL results for optional follow-up `fetch` calls |

Tools are gated per-room via the `com.beeper.ai.tools` state event. `search` may be `off`, `beeper`, or `native`; `fetch` may be `off`, `beeper`, or `native`. The legacy `disabled` array is still read for older room state. In `beeper` mode, web tools route through AI-services (`/tools/web_search`, `/tools/fetch`) using the appservice bearer token. In `native` mode, provider-native tools are injected where supported: OpenAI/OpenRouter search, OpenRouter fetch, Anthropic search/fetch, and Google search/URL context. If the selected provider API has no native equivalent, that native tool is unavailable. Search result URLs stay in the tool view; fetched pages, provider-native citation annotations, URL-context metadata, and final-answer URLs become canonical `com.beeper.source` artifacts for client source cards. Other provider-native built-ins, such as `image_generation`, are still injected from the model catalog (`pkg/connector/builtin_tools.go`).

`fetch` tries the URL directly first with `Accept` preferring Markdown, plain text, JSON, XML, and CSV. If the response is already agent-readable (Markdown/plain/JSON/XML/CSV/source-ish), it returns that result without backend extraction. If the response is HTML, it checks HTTP `Link` headers and HTML `<link rel="alternate" type="text/markdown|text/plain">` for a readable alternate and fetches that directly. Only when the direct representation is not agent-ready does it call AI-services `/tools/fetch`. Local/private hosts, GitHub raw/gist URLs, GitLab-style raw paths, and source/text file extensions are treated as direct-fetch candidates.

**Adding a tool:**

1. Write a constructor returning `agent.AgentTool[any]`; build the schema with `objectSchema(props, required)` and pull args with the `helpers.go` coercers (args arrive as `map[string]any`; JSON numbers are `float64`).
2. Return `jsonResult(value)` for consistent text + `Details` output.
3. Register in `chattools.Tools` (unconditionally or behind a config gate).
4. Wire config in `pkg/connector/chat_tools.go` and honor `DisabledTools`.
5. If it produces citable sources, add canonical source observations in `pkg/connector/sources.go` so URLs surface as message sources.

> **Security note:** the direct fetch path intentionally bypasses AI-services for localhost/private/link-local addresses, raw asset URLs, and source-like files, and has **no SSRF guard**. Deployments that cannot allow bridge-origin egress to private networks should enforce an upstream deny-list or network policy before enabling `fetch`.

## Sessions: the branching conversation tree

A conversation is a **tree of immutable, append-only entries** with a `leaf_id` pointer marking the current head (`pkg/agent/harness/session`). Branching = moving the leaf. Entry types: `message`, `custom_message`, `model_change`, `thinking_level_change`, `compaction`, `branch_summary`, `label`, `session_info`, `custom`, and navigation `leaf` markers.

`BuildContext` walks `leaf → root`, folds the branch into the LLM context, and applies any `compaction` entry (replacing everything before `firstKeptEntryId` with a summary). `Fork` copies a branch into a new session. Entry IDs are time-sortable 8-char UUIDv7 prefixes.

There are **two implementations** of this same tree: per-conversation SQLite files (`session/sqlite_storage.go`) and the shared bridge DB (`pkg/aidb`, tables `ai_session`/`ai_session_entry`). They share the `SessionStorage` interface.

## Compaction

When context approaches the model's window, [`pkg/agent/autocompact`](pkg/agent/autocompact) triggers `harness.Compact`. Two triggers: **overflow** (the provider reported context overflow, or usage exceeded the window) and **threshold** (`contextTokens > contextWindow - ReserveTokens`). Compaction summarizes older history with the LLM, keeps the most recent `KeepRecentTokens`, and writes a `compaction` entry. Defaults: enabled, `ReserveTokens: 16384`, `KeepRecentTokens: 20000`. Token counts are estimates (≈ chars/4; images ≈ 4800 chars), so thresholds are approximate. Users can also `/compact` manually.

## The remote proxy backend

`agent.StreamProxy` (`pkg/agent/proxy.go`) is a drop-in `StreamFn` that proxies streaming to a remote HTTP service (`POST {ProxyURL}/api/stream`, SSE response) instead of calling a provider SDK directly. This lets the bridge centralize provider credentials behind an internal AI-services server — clients hold a proxy token, not raw provider keys. Its output stream is identical to `ai.StreamSimple`, so it's interchangeable anywhere a `StreamFn` is expected.

---

## Login & provider configuration

The bridge advertises five login flows (`pkg/connector/login.go`):

| Flow | What it does |
|------|--------------|
| `beeper` | The default **Beeper AI** login. Loads its catalog and runtime proxy metadata from `ai-services.<domain>` derived from the user's homeserver; uses an appservice bearer token, no stored key. Read-only/managed. |
| `openai-responses` / `openai-completions` / `openai-codex-responses` / `anthropic-messages` / `google-vertex` | **Custom provider**: enter base URL + API key, the bridge loads matching model metadata from ai-services, then you pick a default model. |
| `chatgpt-device` | **ChatGPT** OAuth device-code flow (PKCE). Stores access + refresh tokens, auto-refreshes within 2 min of expiry. |

One Matrix user can hold multiple AI logins; there's a canonical "AI Chats" login per user. Provider configs (with secrets) live in `UserLoginMetadata.Providers`. API keys support `env:NAME` indirection. The `beeper` provider is special and **read-only** — it can't be added/updated/deleted.

Providers can also be managed at runtime:

- **HTTP** (if the Matrix connector supports provisioning): `GET/POST /v3/providers`, `GET/PUT/DELETE /v3/providers/{id}` (optional `?login_id=`).
- **Bridge commands:** `!ai providers`, `!ai provider <show|add|update|delete> …`. `add`/`update` carry a key and redact the command message.

### Slash & bridge commands

Two control surfaces share the same handlers. **Slash commands** are parsed from message bodies; **bridge commands** use the `!ai` prefix. Coverage is intentionally asymmetric.

| Command | Slash | `!ai` | What it does |
|---------|:-----:|:-----:|--------------|
| help | `/help [cmd]` | `!ai ai-help [cmd]` | list/describe commands |
| model | `/model [provider/model]` | `!ai model …` | show or set the room's model (persists `com.beeper.ai.model`) |
| reasoning | `/reasoning [off…xhigh]` | `!ai reasoning …` | show or set reasoning level (validated + clamped to the model) |
| system prompt | `/system-prompt [text\|clear]` | `!ai system-prompt …` | room-specific prompt appended to the default (`com.beeper.ai.additional_prompt`) |
| compact | `/compact [instructions]` | — | manually summarize context |
| abort | `/abort` | — | cancel the active response or compaction; clears queued messages |
| session | `/session` | — | diagnostics: IDs, model, token estimate, message stats, compaction count |
| providers | — | `!ai providers` / `!ai provider …` | list/manage configured providers |

> Unknown `/foo` is **not** a command — it's sent to the model as a prompt. There is no `/new`: start a fresh conversation by creating a new chat (resolve a model contact with `createChat=true`).

---

## Configuration reference

The AI-specific config block (`pkg/connector/config.go`, defaults shown):

```yaml
default_system_prompt: |          # base prompt; room prompts are appended
  You are a helpful assistant running inside the Beeper apps. …
default_reasoning_level: "off"    # off | minimal | low | medium | high | xhigh
fetch:
  timeout_ms: 10000
  max_bytes: 2097152              # 2 MiB
  max_chars: 20000
compaction:
  enabled: true
  reserve_tokens: 16384           # headroom kept below the context window
  keep_recent_tokens: 20000       # recent history preserved verbatim
```

Three scopes layer together: **bridge-wide** YAML → **per-login** provider configs (`UserLoginMetadata.Providers`) → **per-room** state (`com.beeper.ai.model` / `.additional_prompt` / `.tools`).

Relevant constants: default Beeper model `beeper/default`, title-generation model `gpt-4.1-mini` (fallback `gpt-5-mini`), default AI Services base URL derived from the user's homeserver domain.

---

## ID scheme & persistence

All deterministic IDs are built/parsed in [`pkg/aiid`](pkg/aiid):

| Entity | Format |
|--------|--------|
| network / bridge type | `ai` |
| login | `default:<base64url(mxid)>` |
| portal (room) | `mxroom:<base64url(roomID)>` |
| assistant ghost | `assistant:ai` |
| model contact | `model:<encoded model>` for Beeper AI, `model:<encoded provider>:<encoded model>` for custom providers |
| message | `user:<entryID>` / `assistant:<entryID>` |
| media | sanitized parts, or self-describing `ai:<base64url(json)>` |

The **`entryID` is the spine**: generated in the session tree, embedded in the Matrix `MessageID`, stored in the active-stream record, and carried in `MessageMetadata` — so any Matrix event traces back to its exact session-tree node.

Persistence ([`pkg/aidb`](pkg/aidb)) lives in the bridge DB:

- `ai_session` / `ai_session_entry` — the conversation tree.
- `ai_active_stream` — in-flight runs (full `aistream.Run` + metadata + status), so a restart can **resume or finalize** them. Records are upserted on run start and deleted on completion.

`MessageMetadata` (per delivered message) records `SessionEntryID`, `Role`, `RunID`, provider/model, `ResponseID`, `ContentIndex`, `Usage`, `StopReason`, `StreamStatus` — used for resume, editing, and reaction handling.

---

## Testing with the faux provider

[`test/faux-provider`](test/faux-provider) is a standalone Node server that imitates a provider so tests and manual smoke checks don't hit real APIs:

```sh
node test/faux-provider/server.mjs --port 0     # prints {"url":"http://127.0.0.1:PORT"}
# queue a scripted response:
curl -s -X POST "$URL/__faux/responses" -H 'content-type: application/json' \
  --data '[{"content":"hello","stopReason":"stop"}]'
```

It serves `/v1/models`, `/v1/responses`, `/v1/chat/completions`, and `/api/stream` (the proxy SSE shape), plus `/__faux/*` control endpoints. Queued response content blocks mirror `pkg/ai` blocks (`thinking`, `text`, `toolCall`, …), so you can script multi-turn tool-use flows. Run the Go tests with `./test.sh` (which adds `-tags=goolm`).

---

## Quirks & gotchas (read this)

- **Provider registry is populated by import side-effect.** Forget `import _ ".../pkg/ai/providers"` and `ai.Stream` panics.
- **No agent iteration cap.** Bound runs yourself (`ShouldStopAfterTurn`, context deadline, or `Terminate`).
- **`Terminate` needs the whole batch.** One terminating tool alongside non-terminating ones won't stop the loop.
- **Edits rejected, reactions unsupported** as general client affordances.
- **Two delivery modes for the final message** — handle both `inline` and `attachment` (`partsRef`).
- **Per-room config depends on arbitrary-room-state support** in the Matrix connector; writes use a private bridgev2 escape hatch (fragile across upstream changes).
- **The `beeper` provider is read-only** and its base URL may be unavailable (login then fails); its models come from a live catalog, not stored config.
- **Reasoning is double-validated and clamped** — setting a model can silently change the effective reasoning level.
- **Two parallel session-tree implementations** (`aidb` vs `session` SQLite files) with near-duplicate SQL and one subtle difference (`ON DELETE CASCADE`).
- **Token counts are estimates** (≈ chars/4) — compaction thresholds are approximate.
- **The direct fetch path intentionally allows private-network/raw-asset egress and has no SSRF protection.**
- **`ProviderConfig` holds secrets** (API keys, refresh tokens) in login metadata and serializes to JSON *and* YAML — don't log it.
- **AG-UI `Event` is a map, not a struct** — read typed fields via `Get`/`String`; unknown fields survive round-trips.

---

## Package map

| Package | Responsibility |
|---------|----------------|
| `cmd/ai` | bridge entry point (registers connector + providers) |
| `pkg/ai` | provider/API/model abstraction, streaming interface, env keys |
| `pkg/ai/providers` | built-in provider implementations (OpenAI Completions/Responses/Codex, Anthropic, Google GenAI/Vertex) + image generation |
| `pkg/ai-command` | shared slash-command parsing for visible `/...` commands and hidden `!ai ...` command messages |
| `pkg/ai-stream` | the `Run` model: AG-UI event accumulation, anchor/stream/final projection, shared approval commands/coordinator, final-payload sizing |
| `pkg/ag-ui` | the AG-UI wire event protocol, typed events, schema, validation, capabilities |
| `pkg/agent` | autonomous tool-using loop + stateful `Agent` + remote `StreamProxy` |
| `pkg/agent/harness` | production agent: sessions, hooks, queues, compaction, summarization |
| `pkg/agent/harness/session` | branching conversation tree (per-conversation SQLite) |
| `pkg/agent/autocompact` | compaction trigger policy |
| `pkg/chattools` | built-in tools: `get_session`, `fetch`, `web_search` |
| `pkg/connector` | the `bridgev2` connector: rooms↔sessions, command adapters, login, provider catalog loading, capabilities, contacts, direct media, room state |
| `pkg/msgconv` | Matrix ⇄ AI message conversion |
| `pkg/aiid` | deterministic IDs + metadata types |
| `pkg/aidb` | bridge-DB persistence: session storage + active-stream resume |
| `test/faux-provider` | local fake provider for tests & smoke checks |
