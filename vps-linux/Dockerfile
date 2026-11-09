FROM alpine:3.24

ARG TARGETARCH
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 tdm-light \
    && adduser -S -D -H -u 10001 -G tdm-light tdm-light \
    && mkdir -p /data/logs \
    && chown -R tdm-light:tdm-light /data

ENV TZ=Europe/Moscow
COPY --chown=10001:10001 --chmod=0755 tdm-light-linux-${TARGETARCH} /app/tdm-light

USER 10001:10001
WORKDIR /data
ENTRYPOINT ["/app/tdm-light"]
