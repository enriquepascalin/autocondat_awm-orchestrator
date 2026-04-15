# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o awm-orchestrator ./cmd/awm-orchestrator
RUN CGO_ENABLED=0 GOOS=linux go build -o awm-worker ./cmd/awm-worker

# Final stage
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/awm-orchestrator /awm-orchestrator
COPY --from=builder /app/awm-worker /awm-worker
COPY --from=builder /app/workflows /workflows
EXPOSE 9091
USER nonroot:nonroot
ENTRYPOINT ["/awm-orchestrator"]