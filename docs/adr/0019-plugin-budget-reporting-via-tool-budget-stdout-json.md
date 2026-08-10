# Budget reporting for plugin backends uses --tool budget with stdout JSON

The `GET /v1/usage/budget` endpoint supports plugin backends by invoking `--tool budget` on the plugin binary and parsing its stdout as JSON against `assets/budget.schema.json`. The result is included in the response keyed by the backend label.

This is the plugin analogue of the `BudgetReporter` Go interface used by compiled-in providers. A plugin declares it supports budget reporting by listing a `budget` tool in its manifest. If the tool is absent from the manifest, the proxy omits that backend from the budget response (no error).

The stdout-as-JSON convention was chosen because budget data is structured, and structured output over stdout is the lowest-friction contract for a subprocess. stdin/stdout/stderr pass-through (the convention for interactive tooling like `login`) would not work here because the proxy must parse the output programmatically.

## Considered Options

- **Dedicated HTTP endpoint on the sidecar (`GET /v1/budget`)** — considered. This would let the proxy query budget data while the sidecar is running, without forking a subprocess. Rejected because `GET /v1/usage/budget` is an operator-facing endpoint that may be called when the sidecar is not yet running or when the operator wants a snapshot without starting the proxy.
- **Budget data embedded in the `/v1/models` response** — rejected. Budget is operational metadata, not model catalogue data. Mixing them couples unrelated concerns and breaks the clean separation between model discovery and spend reporting.
- **Structured stderr instead of stdout** — rejected. stderr is conventionally for diagnostics and errors; stdout is for machine-readable output. Using stdout for JSON and stderr for error messages follows the Unix convention and is consistent with the `list-models` tool.

## Consequences

Plugin authors who want to surface budget data must emit a JSON object on stdout that satisfies `assets/budget.schema.json` and exit 0. Any non-zero exit or unparseable stdout causes the proxy to omit that backend from the budget response and log the error at WARN.
