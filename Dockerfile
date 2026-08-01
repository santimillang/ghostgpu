# Build the manager binary.
#
# --platform=$BUILDPLATFORM pins this stage to the machine doing the building,
# not the machine being built for. Without it a multi-arch build runs the whole
# compile inside an emulated arm64 container under QEMU, where Go compilation is
# roughly ten to twenty times slower — the first release build spent over twenty
# minutes here. Go cross-compiles to arm64 natively with CGO_ENABLED=0, so the
# emulation bought nothing.
FROM --platform=$BUILDPLATFORM golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# TARGETOS and TARGETARCH are set by buildx and name the platform being built
# FOR, which is what lets a native builder cross-compile. They fall back to the
# builder's own platform for a plain `docker build`, so `make docker-build`
# still produces a binary that runs where it was built.
#
# Deliberately no `-a`: it forces every package including the standard library
# to be rebuilt from scratch on every build, discarding the compiler cache for
# no benefit here.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary.
# Refer to https://github.com/GoogleContainerTools/distroless for more details.
#
# Both base images are pinned by digest, not by tag. The release signs this
# image with cosign, attests its provenance and ships an SBOM - all of which
# describe a build whose inputs would otherwise be mutable. Dependabot tracks
# the docker ecosystem and will raise the bump.
FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
