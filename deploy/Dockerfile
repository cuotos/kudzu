# syntax=docker/dockerfile:1

# Build on the native platform and cross-compile to the target arch — Go with
# CGO disabled cross-compiles without emulation, so multi-arch builds stay fast.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
# Static, stripped binary for the target platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/kudzu ./cmd/kudzu

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kudzu /kudzu
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/kudzu"]
