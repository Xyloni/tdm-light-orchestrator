FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24 AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w" -o /out/tdm-light .

FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 tdm-light \
    && adduser -S -D -H -u 10001 -G tdm-light tdm-light \
    && mkdir -p /data/logs \
    && chown -R tdm-light:tdm-light /data

ENV TZ=Europe/Moscow
COPY --from=build --chown=tdm-light:tdm-light /out/tdm-light /app/tdm-light

USER 10001:10001
WORKDIR /data
ENTRYPOINT ["/app/tdm-light"]
