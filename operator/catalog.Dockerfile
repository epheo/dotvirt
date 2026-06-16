# Serves the File-Based Catalog under catalog/ (built by `make catalog-build`,
# context = operator/). A CatalogSource points at the pushed image; OLM reads the
# package/channel/bundle graph from /configs.
FROM quay.io/operator-framework/opm:v1.47.0
ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs", "--cache-dir=/tmp/cache"]
ADD catalog /configs
# Pre-build the serve cache at image-build time; without it `opm serve` fails its
# startup integrity check (empty /tmp/cache → missing pogreb digest).
RUN ["/bin/opm", "serve", "/configs", "--cache-dir=/tmp/cache", "--cache-only"]
LABEL operators.operatorframework.io.index.configs.v1=/configs
