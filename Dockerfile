FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /postgres-mcp-safe .

FROM gcr.io/distroless/static:nonroot
LABEL io.modelcontextprotocol.server.name="io.github.Poudel0/postgres-mcp-safe"
COPY --from=build /postgres-mcp-safe /postgres-mcp-safe
ENTRYPOINT ["/postgres-mcp-safe"]
