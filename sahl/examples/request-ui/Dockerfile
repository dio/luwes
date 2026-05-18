# Envoy runtime for request-ui.
# The .so is built on the host with:
#   make build-linux-amd64 EXAMPLE=sahl/request-ui
# and bind-mounted into the container by docker-compose.yml.
FROM envoyproxy/envoy:v1.38.0

COPY envoy.yaml /etc/envoy/envoy.yaml

ENV ENVOY_DYNAMIC_MODULES_SEARCH_PATH=/so
ENV GODEBUG=cgocheck=0

EXPOSE 10000 9901 6062
CMD ["envoy", "-c", "/etc/envoy/envoy.yaml", "--log-level", "warning"]
