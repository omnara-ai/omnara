FROM debian:bookworm-slim AS runtime

FROM alpine:3.22
RUN apk add --no-cache curl
ENV HOME=/root
COPY omnarad /usr/local/bin/omnarad
COPY --from=runtime /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2
