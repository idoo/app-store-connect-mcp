# Local source build: docker build -t app-store-connect-mcp .
#
# stdio (default):  docker run -i --rm -e ASC_KEY_ID -e ASC_ISSUER_ID \
#                     -e ASC_PRIVATE_KEY app-store-connect-mcp
# Streamable HTTP (TLS is required with --allow-remote; mount cert and key):
#                   docker run --rm -p 8765:8765 -e ASC_MCP_HTTP_TOKEN \
#                     -e ASC_KEY_ID -e ASC_ISSUER_ID -e ASC_PRIVATE_KEY \
#                     -v "$PWD/tls:/tls:ro" app-store-connect-mcp serve --http 0.0.0.0:8765 \
#                     --allow-remote --tls-cert /tls/cert.pem --tls-key /tls/key.pem
# The bearer token is required either way; see docs/INSTALL.md.
FROM golang:1.27.2@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/app-store-connect-mcp ./cmd/app-store-connect-mcp

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/app-store-connect-mcp /app-store-connect-mcp
USER nonroot:nonroot
EXPOSE 8765
ENTRYPOINT ["/app-store-connect-mcp"]
CMD ["serve"]
