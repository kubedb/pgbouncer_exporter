FROM alpine:3.10
COPY pgbouncer_exporter /usr/bin/
ENTRYPOINT ["pgbouncer_exporter"]
