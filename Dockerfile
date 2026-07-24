FROM golang:1.25-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY skills ./skills
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/fcp ./cmd/fcp

FROM alpine:3.22
RUN addgroup -S fcp && adduser -S -G fcp fcp && mkdir /data && chown fcp:fcp /data
USER fcp
COPY --from=build /out/fcp /usr/local/bin/fcp
EXPOSE 4566 8085
VOLUME ["/data"]
ENTRYPOINT ["fcp"]
CMD ["--listen", "0.0.0.0:4566", "--gcp-listen", "0.0.0.0:8085", "--data-dir", "/data"]
