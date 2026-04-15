# syntax=docker/dockerfile:1

FROM golang:1.26-alpine

RUN apk add --no-cache \
    ca-certificates \
    postgresql-client \
    curl \
    bash

RUN curl -L https://github.com/golang-migrate/migrate/releases/download/v4.17.0/migrate.linux-amd64.tar.gz | tar xvz && \
    mv migrate /usr/local/bin/migrate

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Copy entrypoint script from the central infrastructure directory (relative to build context)
COPY ../../infrastructure/scripts/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 9091

ENTRYPOINT ["/entrypoint.sh"]