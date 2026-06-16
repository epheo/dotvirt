# Serves the File-Based Catalog under catalog/ (built by `make catalog-build`,
# context = operator/). A CatalogSource points at the pushed image; OLM reads the
# package/channel/bundle graph from /configs.
FROM quay.io/operator-framework/opm:v1.47.0
ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs", "--cache-dir=/tmp/cache"]
ADD catalog /configs
LABEL operators.operatorframework.io.index.configs.v1=/configs
