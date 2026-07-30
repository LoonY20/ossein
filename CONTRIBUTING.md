# Contributing to Ossein

Thanks for your interest in Ossein.

Ossein is still in an early design phase, so architecture discussions, API feedback, documentation improvements, tests, and small focused contributions are especially valuable.

## Before you start

Please keep the core project principles in mind:

- convenience without hiding Go;
- standard library compatibility where practical;
- explicit behavior over runtime magic;
- minimal reflection;
- replaceable infrastructure behind clear interfaces;
- performance, debuggability, and maintainability over clever APIs.

For larger changes, open an issue first so the direction can be discussed before significant implementation work begins.

## Development

Requirements:

- Go 1.23 or newer.

Run the checks locally:

```bash
gofmt -w .
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

CI requires at least 85% total statement coverage. Coverage is a guardrail, not
a substitute for behavior-focused tests: prioritize public APIs, concurrency,
error paths, lifecycle behavior, and generated application workflows.

The PostgreSQL and MySQL integration suites are isolated from the
dependency-free core in `integration/postgres` and `integration/mysql`. With
test databases available, run:

```bash
cd integration/postgres
OSSEIN_POSTGRES_DSN='postgres://ossein:ossein@127.0.0.1:5432/ossein_test?sslmode=disable' go test -race ./...

cd ../mysql
OSSEIN_MYSQL_DSN='ossein:ossein@tcp(127.0.0.1:3306)/ossein_test?parseTime=true' go test -race ./...
```

GitHub Actions provisions both database services automatically.

## Pull requests

Keep pull requests focused and easy to review.

A good pull request should:

- explain the problem it solves;
- describe important design choices;
- include tests for behavior changes;
- update documentation when public APIs change;
- avoid unrelated refactors.

Public APIs are not stable yet, so backward compatibility is not guaranteed during the early development phase.

## Commit messages

Conventional-style prefixes are encouraged, for example:

```text
feat: add middleware pipeline
fix: preserve response status
refactor: simplify application lifecycle
docs: document routing API
test: cover route parameters
```

## Code style

Prefer boring, idiomatic Go over framework-specific abstractions.

When choosing between a clever convenience API and an explicit Go API, prefer the design that remains easy to understand with ordinary Go tooling.

## Code of Conduct

By participating in this project, you agree to follow the repository's [Code of Conduct](CODE_OF_CONDUCT.md).
