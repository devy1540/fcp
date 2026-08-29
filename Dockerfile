FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY skills ./skills
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/fcp ./cmd/fcp

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN addgroup -S fcp && adduser -S -G fcp fcp && mkdir /data && chown fcp:fcp /data
USER fcp
COPY --from=build /out/fcp /usr/local/bin/fcp
EXPOSE 4566 8085
VOLUME ["/data"]
HEALTHCHECK --interval=2s --timeout=2s --start-period=2s --retries=20 \
  CMD ["fcp", "doctor", "--endpoint", "http://127.0.0.1:4566", "--gcp-endpoint", "127.0.0.1:8085", "--timeout", "1s", "--json"]
ENTRYPOINT ["fcp"]
CMD ["--listen", "0.0.0.0:4566", "--gcp-listen", "0.0.0.0:8085", "--data-dir", "/data"]
