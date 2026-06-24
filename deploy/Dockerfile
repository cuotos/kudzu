# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, stripped binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kudzu ./cmd/kudzu

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kudzu /kudzu
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/kudzu"]
