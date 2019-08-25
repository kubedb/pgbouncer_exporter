FROM alpine:3.10
COPY pgbouncer_exporter_tmp_bin /usr/bin/
ENTRYPOINT ["pgbouncer_exporter_tmp_bin"]
