# opencode integration examples

Drop-in artefacts for displaying spend / quota inside an
[opencode](https://opencode.ai) session via this proxy's
[`/v1/usage/budget`](../../docs/configuration.md#budget--quota-reporting)
endpoint.

## Files

- **`command/llm-budget.md`** — opencode slash command (`/llm-budget`) that
  curls the proxy and prints each configured backend's spend / quota.

## Installing

The proxy must already be running locally and have at least one backend
that reports budget configured (see
[docs/providers/ibm-bob.md](../../docs/providers/ibm-bob.md#budget-reporting)
or
[docs/providers/github-copilot.md](../../docs/providers/github-copilot.md#budget--premium-request-reporting)).
By default the proxy binds to `127.0.0.1:4980`; the examples assume that.
If you've moved it, change the URL in the command body.

### Workspace-scoped (this project only)

```sh
mkdir -p .opencode/command
cp examples/opencode/command/llm-budget.md .opencode/command/
```

### User-scoped (all opencode sessions)

```sh
mkdir -p ~/.config/opencode/command
cp examples/opencode/command/llm-budget.md ~/.config/opencode/command/
```

## Usage

Inside an opencode session:

```
/llm-budget
```

opencode executes the embedded shell command and surfaces its output. The
proxy fetches `/v1/usage/budget` on every call (no caching) so the number is
live.

## Customising

The slash command is plain Markdown with YAML front matter and a single
shell substitution (`` !`...` ``). Edit `command/llm-budget.md` freely; common
tweaks:

- Switch from `127.0.0.1:4980` to your actual bind address.
- Add a warning threshold:

  ```sh
  !`curl -sf http://127.0.0.1:4980/v1/usage/budget \
    | jq -r '.data[] | select(.remaining < 10) | "WARNING: \(.backend): \(.remaining) \(.currency) left"'`
  ```

- Focus on one backend (add `?backend=<prefix>`):

  ```sh
  !`curl -sf http://127.0.0.1:4980/v1/usage/budget?backend=copilot \
    | jq -r '"\(.backend): \(.remaining) \(.currency)"'`
  ```
