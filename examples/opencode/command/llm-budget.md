---
description: Show remaining quota for configured LLM backends via the local llm-proxy.
---

Current budget for this proxy's configured backends:

!`curl -sf 'http://127.0.0.1:4980/v1/usage/budget' \
  | jq -r '.data[] | if .unlimited then "\(.backend): \(.spend) spent (unlimited \(.currency))" else "\(.backend): \(.spend) spent / \(.max_budget) \(.currency) -> \(.remaining) remaining" end'`
