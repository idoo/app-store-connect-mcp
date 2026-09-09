# Used by goreleaser (dockers_v2): the build context holds the prebuilt
# binary at $TARGETPLATFORM/hamn. For a local source build use ./Dockerfile.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/hamn /hamn
USER nonroot:nonroot
EXPOSE 8765
ENTRYPOINT ["/hamn"]
CMD ["serve"]
