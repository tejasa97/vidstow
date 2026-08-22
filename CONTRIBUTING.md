# Contributing to VidStow

Keep changes focused on the Desktop product and preserve the deliberate
boundary between the UI workflow and the engine/provider composition.
VidStow must continue to use the public engine + providers/youtube packages
from the ytdlp-go module github.com/tejasa97/youtube_dlp; do not reach into its
internal packages or the broad pkg/ytdlp facade.

Before submitting a change:

~~~sh
gofmt -w .
go mod tidy -diff
go vet ./...
go test -count=1 ./...
go build ./...

cd frontend
npm ci
npm run check
npm run test:ui
npm run build
~~~

Run wails build when a native build is available on the host. Keep UI
validation and request mapping narrow; do not add hidden source-specific
feature gates to the engine composition.

By contributing, you agree that your contribution is available under the
repository's Apache-2.0 license.
