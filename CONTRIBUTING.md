# Contributing to pingtxt-telemetry

Thanks for helping. The usage stats service is a small companion service of [pingtxt](https://github.com/federico-it/pingtxt),
so most features and bug reports belong there: open them in
[pingtxt's issues](https://github.com/federico-it/pingtxt/issues) unless the problem is clearly in this
service.

- **Security issue?** Do not open a public issue. Follow [SECURITY.md](SECURITY.md).
- For anything bigger than a small fix, open an issue first and agree on the approach.
- Remove tokens, secrets and IPs you don't want public from logs and screenshots.
- Never commit credentials (`.env`, database files).

## Development

You need **Go** (the version in [go.mod](go.mod)). Docker is optional.

```sh
go test -race ./...
TELEMETRY_DATA=./data go run ./cmd/pingtxt-telemetry
docker build -t pingtxt-telemetry .
```

## Pull requests

- Keep each pull request to one change, with tests for new behavior.
- Run `gofmt -l .`, `go vet ./...` and `go test -race ./...` before pushing: CI runs the same checks
  and builds the Docker image.
- Update README.md, and docs/openapi.json when a route changes (a test checks it matches the server).
- By contributing you agree that your work is licensed under the [AGPL-3.0](LICENSE), like the rest of
  the project. The [pingtxt code of conduct](https://github.com/federico-it/pingtxt/blob/main/CODE_OF_CONDUCT.md)
  applies here too.
