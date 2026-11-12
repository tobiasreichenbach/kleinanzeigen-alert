FROM golang:1.22-alpine AS build
RUN apk add --no-cache gcc musl-dev
# old go-sqlite3 needs this to compile against current musl
ENV CGO_ENABLED=1 CGO_CFLAGS=-D_LARGEFILE64_SOURCE
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /out/kleinanzeigen-alert .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/kleinanzeigen-alert /usr/local/bin/kleinanzeigen-alert
ENTRYPOINT ["/usr/local/bin/kleinanzeigen-alert"]
