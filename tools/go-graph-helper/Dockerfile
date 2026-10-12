# Build from the repository root. The builder must be an operator-selected
# immutable image reference, e.g. golang:<version>@sha256:<digest>.
ARG VIBER_GRAPH_BUILDER_IMAGE
FROM ${VIBER_GRAPH_BUILDER_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test "$(go env GOVERSION)" = "go1.27.2" && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
    go build -trimpath -buildvcs=false -ldflags='-buildid=' -o /out/viber-go-graph-helper ./cmd/viber-go-graph-helper

FROM scratch
COPY --from=build /out/viber-go-graph-helper /usr/local/bin/viber-go-graph-helper
USER 65532:65532
