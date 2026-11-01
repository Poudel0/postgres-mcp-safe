FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /postgres-mcp-safe .

FROM gcr.io/distroless/static:nonroot
LABEL io.modelcontextprotocol.server.name="io.github.Poudel0/postgres-mcp-safe"
LABEL org.opencontainers.image.source="https://github.com/Poudel0/postgres-mcp-safe"
COPY --from=build /postgres-mcp-safe /postgres-mcp-safe
ENTRYPOINT ["/postgres-mcp-safe"]
