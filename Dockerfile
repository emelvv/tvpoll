FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/load ./cmd/load

FROM alpine:3.22
RUN addgroup -g 10001 app && adduser -D -H -u 10001 -G app app
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/load /usr/local/bin/load
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["server"]
