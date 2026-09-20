# Local source build: docker build -t hamn .
#
# stdio (default):  docker run -i --rm -e ASC_KEY_ID -e ASC_ISSUER_ID \
#                     -e ASC_PRIVATE_KEY hamn
# Streamable HTTP:  docker run --rm -p 127.0.0.1:8765:8765 -e HAMN_HTTP_TOKEN \
#                     -e ASC_KEY_ID -e ASC_ISSUER_ID -e ASC_PRIVATE_KEY \
#                     hamn serve --http 0.0.0.0:8765 --allow-remote
# Publish the port on loopback only and put a TLS proxy in front of anything
# reachable from another host; the bearer token is required either way.
FROM golang:1.26.10@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hamn ./cmd/hamn

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/hamn /hamn
USER nonroot:nonroot
EXPOSE 8765
ENTRYPOINT ["/hamn"]
CMD ["serve"]
