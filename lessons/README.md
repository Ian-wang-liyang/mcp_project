# Lessons

Short reference lessons for the concepts the milestones use. Each file opens with a **primer** (the big picture), then
numbered lessons. A lesson explains one concept (CONCEPT, FROM PYTHON/JS, HINT), states a small task (TASK), and shows
one way to solve it (**Worked example**). The briefs in `milestones/` cite them by number, e.g. "lesson 12.5".

The lessons started life as test-driven drills. Where one says "the tests", it means that drill's own tests, which
aren't part of this repository. Your tests here are the acceptance tests in `acceptance/`.

| File | Topic | Used by |
|---|---|---|
| [`08_sync_context.md`](08_sync_context.md) | mutexes, `sync.Once`, context cancellation | milestones 4, 5 |
| [`10_mcp_basics.md`](10_mcp_basics.md) | what MCP is; your first server and client | milestone 1 |
| [`11_mcp_tools.md`](11_mcp_tools.md) | tools in depth: schemas, structured output, tool errors, annotations | milestones 1, 2, 6 |
| [`12_mcp_resources.md`](12_mcp_resources.md) | resources, templates, blobs, subscriptions | milestone 3 |
| [`13_mcp_prompts.md`](13_mcp_prompts.md) | prompts and argument completion | milestone 3 |
| [`14_mcp_client.md`](14_mcp_client.md) | the client side: pagination, progress, logging, cancellation | milestone 4 |
| [`15_mcp_server_calls.md`](15_mcp_server_calls.md) | server → client requests: roots, sampling, elicitation, MRTR | milestone 6 |
| [`16_mcp_middleware.md`](16_mcp_middleware.md) | middleware around MCP messages | milestone 5 |
| [`17_mcp_capstone.md`](17_mcp_capstone.md) | real transports: Streamable HTTP and stdio | milestone 5 |

The rest of the original set covered general Go and isn't included: the milestones assume basic Go.
