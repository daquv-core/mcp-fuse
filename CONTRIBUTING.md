# Contributing

Thank you for helping. mcp-fuse is small on purpose, so the first thing to know is what it will not become.

## Scope

mcp-fuse presents several MCP servers as one and does nothing else. These will not be added, whoever asks and
however reasonable they sound; they belong in the backends:

- authentication, token issuing or validation of its own, or passing a token on;
- policy, masking, result transformation, caching of results, retries, any LLM;
- understanding what a tool means;
- logging request or response bodies, or tokens.

A change that keeps the code readable in an afternoon, so that someone can verify it does not read or keep what it
carries, is welcome. A change that grows the surface is better made as a separate project in front of or behind it.

Open an issue before a large change.

## Working on it

```sh
./build.sh              # gofmt check, go vet, static build
go test -race ./...
```

Go standard library only; do not add a dependency. The layout is in [CLAUDE.md](CLAUDE.md): dependencies point one
way, from `internal/fuse` to the other packages.

## What a change needs

- **Tests.** The invariants listed in [CLAUDE.md](CLAUDE.md) are each held by a test that fails when the behaviour is
  removed; keep it that way. Break your own change on purpose and check a test notices. A test that passes for a
  reason other than the one it names is worse than none.
- **Documentation** in step with the code. `README.md` is the source for `README.ko.md` and `README.ja.md`; change
  all three together. The documents under `docs/` are in English and state what is true whatever the release — no
  "not yet" or "coming soon".
- **A small diff.** Keep a change to one thing.

## Security

Do not open a public issue for a vulnerability; see [SECURITY.md](SECURITY.md).
