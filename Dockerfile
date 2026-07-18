# syntax=docker/dockerfile:1
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -o /out/email_worker ./cmd/email_worker

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/email_worker /usr/local/bin/email_worker
USER nonroot:nonroot
# Default to the server; override `command` for the worker.
ENTRYPOINT ["/usr/local/bin/server"]
