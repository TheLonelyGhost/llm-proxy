# Upstream model discovery is a plugin concern; list-models tool bypasses the allow-list

The proxy has no built-in upstream model discovery command. Plugins that can enumerate available models from their upstream should expose a `list-models` tool:

```sh
llm-proxy plugin run <backend-label> list-models
```

This replaces the former `llm-proxy providers list-models <backend>` command. The critical behavioural property is preserved: `list-models` output must **not** be filtered by the operator's `models` allow-list. The allow-list is a serve-time concern applied by the proxy; `list-models` is a discovery tool for operators deciding what to put in the allow-list.

## Considered Options

- **Keep `llm-proxy providers list-models` as a built-in command** — rejected. The command invoked `Backend.Models()` on the compiled-in provider, which cannot work for external plugin backends. Delegating to `--tool list-models` gives every plugin, compiled-in or external, a uniform discovery path.
- **Expose model discovery via a dedicated HTTP endpoint the proxy calls** — rejected. Model discovery is an operator-facing, pre-serve workflow. Requiring the proxy to be running before discovery can happen creates a chicken-and-egg problem (you need to know which models to allow-list before you configure the proxy, but you need the proxy running to query them).
- **Filter `list-models` output through the allow-list** — rejected. The entire point of the tool is to help the operator construct or verify the allow-list. Filtering by the allow-list would make it impossible to discover models not yet on the list.

## Consequences

Plugins backed by a static, well-known model list do not need this tool. The `--tool list-models` convention is advisory; the proxy does not require it to be present.
