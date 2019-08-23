FROM alpine:3.10
COPY pb_prom_exporter /usr/bin/
ENTRYPOINT ["pb_prom_exporter"]