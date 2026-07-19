# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dday .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /dday /dday
EXPOSE 3329
USER nonroot:nonroot
ENTRYPOINT ["/dday"]
