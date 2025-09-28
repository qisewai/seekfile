# syntax=docker/dockerfile:1
FROM golang:1.24-bullseye AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o /out/seekfile ./cmd/seekfile

FROM debian:bookworm-slim
RUN useradd -r -u 10001 seekfile
WORKDIR /srv/seekfile
COPY --from=build /out/seekfile /usr/local/bin/seekfile
VOLUME ["/data", "/config"]
EXPOSE 8080
USER seekfile
ENTRYPOINT ["/usr/local/bin/seekfile"]
CMD ["-config", "/config/seekfile.config.json"]
