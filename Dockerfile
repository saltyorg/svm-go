# Build stage
FROM golang:1.24-alpine AS builder

# Set environment variables
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

# Set work directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN go build -ldflags="-w -s" -o svm main.go

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Set work directory
WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/svm .

# Expose the port
EXPOSE 8000

# Set environment variable for port (matching the original)
ENV PORT=8000

# Start the application (matching the original uvicorn command structure)
CMD ["./svm"]
