# See order-service/Dockerfile for the commented walkthrough; this is identical
# except for the binary name and exposed port.
FROM golang:1.24 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /bin/payment-service .

FROM gcr.io/distroless/static-debian12
COPY --from=build /bin/payment-service /bin/payment-service
EXPOSE 8081
ENTRYPOINT ["/bin/payment-service"]
