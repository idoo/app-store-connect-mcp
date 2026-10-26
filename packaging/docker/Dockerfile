# Used by goreleaser (dockers_v2): the build context holds the prebuilt
# binary at $TARGETPLATFORM/app-store-connect-mcp. For a local source build use ./Dockerfile.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/app-store-connect-mcp /app-store-connect-mcp
USER nonroot:nonroot
EXPOSE 8765
ENTRYPOINT ["/app-store-connect-mcp"]
CMD ["serve"]
