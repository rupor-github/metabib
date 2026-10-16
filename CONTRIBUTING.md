# Contributing to metabib

Contributions are welcome, including bug reports, documentation improvements, tests, and code changes. Please read these guidelines before opening an issue or pull request.

## Before you begin

- Read the [README](README.md), especially the project scope and the documentation relevant to your change.
- Search existing [issues](https://github.com/rupor-github/metabib/issues) and [pull requests](https://github.com/rupor-github/metabib/pulls) to avoid duplicates.
- Any code change, including a simple bug fix, requires an open issue first. Open an issue or join an existing one to discuss the problem and proposed approach before working on a code contribution.
- Documentation-only corrections may be submitted directly as pull requests without an issue.

An issue should explain the problem and intended outcome without requiring maintainers to infer them from a patch. Reviewing code, especially AI-generated code, can take more effort than writing a fix. Discussing the intent first helps relate a proposed change to the project's history and direction.

## Reporting bugs

Include the metabib version, operating system and architecture, exact command, relevant configuration, and available error messages. Describe the expected behavior and actual result, and provide steps to reproduce the problem.

- Use global `--verbose` to capture detailed progress when useful. For example, rerun the failing command as `metabib --verbose cache ...`, keeping its original arguments.
- To capture the effective configuration, run `metabib --config metabib.yaml dumpconfig effective.yaml` with the configuration file used by the failing command. See [Configuration](README.md#configuration).
- For database issues, include the MariaDB version, whether you use a managed instance or an existing service, and the relevant dump names and dates.
- For archive, FB2, dataset, or INPX issues, provide a minimal sample when possible, identifying the affected archive entries or book IDs and the input/output formats involved.

**Review logs, configuration, and samples before sharing:** they can contain database credentials, private paths, source URLs, or book content. Remove private information and only share material you have permission to distribute. Prefer a small, shareable reproduction over uploading entire libraries or database dumps.

## Suggesting features

Describe the use case, the problem with the current behavior, and the result you would like to see. Include examples and alternatives where useful. Check the [project scope](README.md#metabib) first; opening an issue does not guarantee that a feature will be implemented.

## Development and testing

The development tasks assume a Linux host. Use the Go version specified in [`go.mod`](go.mod); the project includes [Task](https://taskfile.dev/) and staticcheck as Go tools. The maintainer workflow uses [Jujutsu (`jj`)](https://jj-vcs.github.io/jj/).

From the repository root:

```sh
# Build build/metabib and run static analysis (does not run tests).
go tool task

# Compile and test all packages through Task, generating version metadata first.
PACKAGES='./...' go tool task test

# Run static analysis separately.
go tool task lint
```

The default build requires `pre-commit` and installs hooks. Tasks generate the ignored `misc/version.go`; generate it through a build or test task before using direct Go build/test commands in a fresh checkout. See [`Taskfile.yaml`](Taskfile.yaml) and [`.pre-commit-config.yaml`](.pre-commit-config.yaml) for prerequisites and details.

After version metadata exists, `go test -mod=mod ./...` also compiles and tests all packages. Keep `-mod=mod` when running Go commands directly, since a stale local `vendor/` directory may exist. Plain `go tool task test` only visits packages containing `*_test.go`; the `PACKAGES='./...'` override above includes every package.

MariaDB integration tests are opt-in and require the [MariaDB binaries](README.md#mariadb-binaries). They start private temporary instances rather than using an existing database:

```sh
METABIB_TEST_MARIADB=1 go test -mod=mod ./db

# Additionally exercise annotation lookups against an actual SQL dump.
METABIB_TEST_ANNOTATION_DUMP=/path/to/lib.b.annotations.sql go test -mod=mod ./db -run TestAnnotationReadIndexMariaDB
```

Follow the surrounding code style. Format changed Go files with `goimports-reviser`:

```sh
GOFLAGS=-mod=mod goimports-reviser -format -set-alias -company-prefixes github.com/rupor-github path/to/changed.go
gopls check -severity=hint path/to/changed.go
```

Use `gofmt` as a fallback only if `goimports-reviser` is unavailable. Add or update tests for behavior changes, and include a regression test for bug fixes where practical. Update documentation and the relevant [JSON schemas](docs/) when changing user-visible behavior or data formats.

## Submitting a pull request

For any pull request containing code changes, link the issue where the problem and proposed approach were discussed first. Only documentation-only corrections are exempt from this requirement.

1. Fork the repository and prepare your change.
2. Keep the pull request focused on one issue; avoid unrelated refactoring or formatting changes.
3. Use clear commit messages explaining the purpose of the changes.
4. Run the relevant tests and checks. State which checks you ran and explain any failures, skipped integration tests, or checks you could not run.
5. Open a pull request describing the problem, your solution, and any compatibility implications. Link related issues and include before-and-after examples when helpful.
6. Respond to review feedback and update your change as needed.

Do not include build artifacts, generated `misc/version.go`, private data, database directories, downloaded dumps, book archives, or generated datasets. For metadata or INPX changes, validate the affected output and describe any schema or consumer compatibility implications.

Maintainers may request changes or decline contributions that do not fit the project's scope or maintenance needs. Reviews are provided as time permits; there is no guaranteed response time.

## AI-assisted contributions

AI-assisted contributions are welcome, provided a human remains actively involved throughout the process. You are responsible for everything you submit, regardless of how it was produced.

- Review and fully understand all submitted code, documentation, and tests.
- Verify the result and its relevance to the issue, rather than relying on the AI's claims.
- Be prepared to explain your choices, discuss trade-offs, and respond meaningfully to review feedback.
- The issue-first requirement for code changes applies equally to AI-assisted contributions.

Blindly copying AI-generated material does not help the project. Do not submit contributions you cannot understand, explain, or support in discussion; maintainers should not have to reverse-engineer their intent or act as the first human reviewer of unchecked AI output.

## Community conduct

Be respectful and constructive in issues, pull requests, and reviews. Discuss ideas and code rather than making personal remarks. Harassment and abusive behavior are not acceptable.

## Licensing

By submitting a contribution, you agree that it may be distributed under the project's [GNU General Public License v3.0](LICENSE). Only submit work you have the right to contribute, and preserve applicable copyright notices and third-party license information.
