# cp_share - the contract with control-plane

Everything shared with CP, and it is self-contained: someone who sees only this repository must be able to
build, deploy and verify mcp-fuse from what is here.

- [`contract.md`](contract.md) - runtime, configuration, front proxy, build and handover, prerequisites.

## In one line
mcp-fuse is **a static binary bound to loopback** that makes several MCP servers look like one. The nginx in
front owns TLS and the path; mcp-fuse owns nothing else - it does not authenticate, store or log anyone's data.
