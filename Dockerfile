FROM golang:1.25.12-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build
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
ENTRYPOINT ["fcp"]
CMD ["--listen", "0.0.0.0:4566", "--gcp-listen", "0.0.0.0:8085", "--data-dir", "/data"]
