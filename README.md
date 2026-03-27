# resolve-cli

CLI tool for resolving package dependencies using real package managers. Part of [ecosyste.ms](https://ecosyste.ms).

Given a package name and registry (or ecosystem), it fetches the package's runtime dependencies from the ecosyste.ms API, creates a temporary project, runs the actual package manager to resolve the full dependency tree, and outputs the result as JSON.

## Usage

```
resolve --registry rubygems.org --package rails --version 7.0.0
resolve --ecosystem npm --package express --version 4.21.2
resolve --ecosystem gem --package sinatra --tree
resolve --ecosystem cargo --package serde --before 2024-01-01
```

### Flags

| Flag | Description |
|------|-------------|
| `--registry` | ecosyste.ms registry name (e.g. rubygems.org, npmjs.org) |
| `--ecosystem` | PURL type (e.g. gem, npm, cargo, pypi). Alternative to --registry |
| `--package` | Package name (required) |
| `--version` | Version to resolve (default: latest) |
| `--tree` | Output full dependency tree with PURLs instead of flat map |
| `--before` | Only consider versions published before this date (ISO 8601) |
| `--manager` | Override the package manager (e.g. uv instead of pip) |
| `--timeout` | Timeout in seconds (default: 120) |
| `--api-key` | ecosyste.ms API key (also reads ECOSYSTEMS_API_KEY env var) |

Either `--registry` or `--ecosystem` is required.

### Output

Flat (default):

```json
{
  "connection_pool": "3.0.2",
  "redis-client": "0.28.0"
}
```

Tree (`--tree`):

```json
[
  {
    "purl": "pkg:gem/redis-client@0.28.0",
    "name": "redis-client",
    "version": "0.28.0",
    "deps": [
      {
        "purl": "pkg:gem/connection_pool@3.0.2",
        "name": "connection_pool",
        "version": "3.0.2"
      }
    ]
  }
]
```

## How it works

1. Fetches the package's runtime dependencies from the [packages.ecosyste.ms](https://packages.ecosyste.ms) API
2. Determines the appropriate package manager using the registry's PURL type
3. Creates a temporary project directory
4. Runs `init` and `add` for each dependency using [git-pkgs/managers](https://github.com/git-pkgs/managers)
5. Runs the manager's `resolve` command to produce the dependency graph
6. Parses the output using [git-pkgs/resolve](https://github.com/git-pkgs/resolve) into a normalized format with PURLs

## Supported ecosystems

npm, gem, cargo, pypi, golang, maven, packagist, pub, hex, nuget, swift, clojars, hackage, conda, deno, helm, conan

Multiple managers are supported per ecosystem (e.g. npm/yarn/pnpm/bun for npm, pip/uv/poetry for pypi). Use `--manager` to override the default.

## Installation

```
go install github.com/ecosyste-ms/resolve-cli/cmd/resolve@latest
```

Or build from source:

```
git clone https://github.com/ecosyste-ms/resolve-cli.git
cd resolve-cli
go build -o resolve ./cmd/resolve
```

## License

AGPL-3.0-or-later
