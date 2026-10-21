FROM golang:1.27-alpine3.24 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/mig-scaler ./cmd/mig-scaler

FROM alpine:3.24

RUN addgroup -S scaler && adduser -S -G scaler scaler

COPY --from=build /out/mig-scaler /usr/local/bin/mig-scaler

EXPOSE 8080
USER scaler
ENTRYPOINT ["mig-scaler"]
