## ADDED Requirements

### Requirement: /v1/models returns exact OpenAI specification format

The system SHALL return `/v1/models` response exactly as OpenAI specifies, without additional fields.

#### Scenario: /v1/models response structure

- **WHEN** client requests GET `/v1/models`
- **THEN** system returns JSON with `object: "list"` and `data` array containing objects with only `id`, `object`, `created`, `owned_by` fields

#### Scenario: /v1/models excludes capability metadata

- **WHEN** client requests GET `/v1/models`
- **THEN** response SHALL NOT include `capabilities`, `vendor`, `version`, or other extended fields in model objects

#### Scenario: /v1/models aggregates across backends

- **WHEN** multiple backends are configured
- **THEN** system aggregates models from all backends with prefixed model IDs (e.g., `copilot/gpt-4o`, `openai/gpt-4o`)

### Requirement: /v1/model/info provides capability discovery

The system SHALL provide a `/v1/model/info` endpoint that returns rich model metadata including capabilities.

#### Scenario: /v1/model/info response structure

- **WHEN** client requests GET `/v1/model/info`
- **THEN** system returns JSON with `object: "list"` and `data` array containing ModelInfo objects with `id`, `name`, `vendor`, `capabilities`, `token_limits`, `reasoning_effort_levels`, `deprecated`, `version`, `extras` fields

#### Scenario: Capabilities derived from upstream providers

- **WHEN** backend implements ListUpstreamModels
- **THEN** system extracts capabilities from upstream provider response and includes in `/v1/model/info` data

#### Scenario: Model-specific capability differences

- **WHEN** same base model exists in multiple backends (e.g., `copilot/gpt-4o` and `openai/gpt-4o`)
- **THEN** each model MAY have different capabilities based on backend limitations

#### Scenario: Example capability differences

- **WHEN** comparing `copilot/gpt-4o` and `openai/gpt-4o`
- **THEN** `copilot/gpt-4o` may have `["chat", "tools", "streaming"]` while `openai/gpt-4o` includes `["chat", "tools", "vision", "streaming"]` if Copilot backend does not support vision

### Requirement: Backend interface returns ModelInfo

The system SHALL change `Backend.Models()` to return `[]ModelInfo` instead of `[]Model`.

#### Scenario: Models method returns ModelInfo

- **WHEN** server calls `Backend.Models(ctx)`
- **THEN** backend returns slice of ModelInfo with ID, Name, Vendor, Capabilities, Deprecated, Version, Extras fields

#### Scenario: Backends delegate to ListUpstreamModels

- **WHEN** backend implements both `Models()` and `ListUpstreamModels()`
- **THEN** `Models()` implementation SHALL delegate to `ListUpstreamModels()` to avoid duplication

#### Scenario: Minimal ModelInfo for simple backends

- **WHEN** backend cannot query upstream metadata
- **THEN** backend MAY return ModelInfo with only ID and Vendor populated, leaving other fields empty

### Requirement: Deprecate Model type

The system SHALL remove the simplified `Model` type from backend interface.

#### Scenario: Model type no longer used

- **WHEN** code references backend models
- **THEN** system uses ModelInfo exclusively, not Model type

### Requirement: Capability field standardization

The system SHALL use standardized capability string values across all backends.

#### Scenario: Recognized capability values

- **WHEN** backend populates capabilities field
- **THEN** system uses standard values: `chat`, `completions`, `embeddings`, `tools`, `vision`, `reasoning`, `streaming`, `audio.speech`, `audio.transcriptions`, `images.generations`, `moderations`

#### Scenario: Backend-specific capabilities preserved

- **WHEN** upstream provider reports non-standard capabilities
- **THEN** system preserves those capability strings in Extras field with `capability_` prefix

### Requirement: Token limits in ModelInfo

The system SHALL include token limits in ModelInfo when upstream provider exposes them.

#### Scenario: TokenLimits struct populated

- **WHEN** backend extracts token limits from upstream metadata
- **THEN** system populates TokenLimits struct with MaxInputTokens, MaxOutputTokens, MaxContextTokens

#### Scenario: TokenLimits optional

- **WHEN** upstream provider does not expose token limit information
- **THEN** ModelInfo.TokenLimits SHALL be nil

#### Scenario: Partial token limits

- **WHEN** upstream provides some but not all token limit fields
- **THEN** system populates available fields and sets unavailable fields to 0

### Requirement: Reasoning effort levels in ModelInfo

The system SHALL include supported reasoning effort levels for models with reasoning capability.

#### Scenario: ReasoningEffortLevels populated for reasoning models

- **WHEN** model includes "reasoning" in capabilities
- **THEN** ModelInfo.ReasoningEffortLevels SHALL contain supported effort levels (e.g., ["low", "medium", "high"])

#### Scenario: ReasoningEffortLevels empty for non-reasoning models

- **WHEN** model does not include "reasoning" in capabilities
- **THEN** ModelInfo.ReasoningEffortLevels SHALL be nil or empty

#### Scenario: OpenAI o1/o3 models reasoning effort

- **WHEN** OpenAI backend queries o1-preview, o1-mini, o3-mini models
- **THEN** system populates ReasoningEffortLevels with ["low", "medium", "high"]

### Requirement: Copilot backend parses upstream capabilities

The system SHALL extract capabilities from GitHub Copilot's model catalog response.

#### Scenario: Copilot capabilities extraction

- **WHEN** Copilot backend calls ListUpstreamModels
- **THEN** system parses `capabilities.type` and `capabilities.supports` fields from upstream response

#### Scenario: Copilot token limits extraction

- **WHEN** Copilot backend parses upstream model response
- **THEN** system extracts token limits from `capabilities.limits` object (e.g., max_prompt_tokens, max_completion_tokens)

#### Scenario: Copilot vision limitation

- **WHEN** Copilot backend queries models
- **THEN** system SHALL NOT include `vision` capability even if base model supports it, because Copilot strips image inputs

### Requirement: OpenAI backend extracts available metadata

The system SHALL extract metadata from OpenAI's `/v1/models` endpoint when available.

#### Scenario: OpenAI metadata extraction

- **WHEN** OpenAI backend calls ListUpstreamModels
- **THEN** system extracts vendor, created timestamp, owned_by from upstream response

#### Scenario: OpenAI token limits for known models

- **WHEN** OpenAI backend processes models with known token limits
- **THEN** system populates TokenLimits based on model family (e.g., gpt-4o: 128k context, gpt-3.5-turbo: 16k context)

#### Scenario: OpenAI reasoning models

- **WHEN** OpenAI backend encounters o1-preview, o1-mini, o3-mini models
- **THEN** system includes "reasoning" in capabilities and populates ReasoningEffortLevels with ["low", "medium", "high"]

#### Scenario: OpenAI lacks capability metadata

- **WHEN** OpenAI's upstream `/v1/models` does not include capability information
- **THEN** system returns ModelInfo with empty Capabilities field unless enriched from other sources

### Requirement: IBM Bob backend extracts capabilities

The system SHALL extract capabilities from IBM Bob's model info endpoint.

#### Scenario: IBM Bob capabilities from model info

- **WHEN** IBM Bob backend calls ListUpstreamModels
- **THEN** system extracts available capabilities from `/v1/model/info` or equivalent endpoint response

#### Scenario: IBM Bob token limits extraction

- **WHEN** IBM Bob backend queries model info endpoint
- **THEN** system extracts token limits from upstream response if present (e.g., max_tokens, context_length fields)
