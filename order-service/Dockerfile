# Multi-stage build: compile in the full Go image, then ship a tiny final image.
#
# Stage 1: build a statically linked binary.
FROM golang:1.24 AS build
WORKDIR /src

# Copy module files first so Docker can cache `go mod download` across rebuilds
# that only change source code.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 makes a static binary so it runs in a minimal base image.
RUN CGO_ENABLED=0 go build -o /bin/order-service .

# Stage 2: minimal runtime image. `distroless/static` has no shell and no
# package manager — a smaller attack surface and a good thing to be aware of
# when you later try to `docker exec` in and find there is no /bin/sh.
FROM gcr.io/distroless/static-debian12
COPY --from=build /bin/order-service /bin/order-service
EXPOSE 8080
ENTRYPOINT ["/bin/order-service"]
