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

# Copy only the service source first (for better caching)
COPY services/awm-orchestrator/go.mod services/awm-orchestrator/go.sum ./
RUN go mod download

COPY services/awm-orchestrator/ ./

# Copy entrypoint script from root scripts folder
COPY scripts/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 9091

ENTRYPOINT ["/entrypoint.sh"]