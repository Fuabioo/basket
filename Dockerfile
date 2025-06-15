# Simple development Dockerfile
FROM golang:1.24.4-alpine3.22 AS builder
WORKDIR /app
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 go build -o basket .

FROM scratch
COPY --from=builder /app/basket /app/
COPY --from=builder /app/assets /app/assets
WORKDIR /app
CMD ["./basket"]
