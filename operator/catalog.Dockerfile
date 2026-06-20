# Serves the File-Based Catalog under catalog/ (built by `make catalog-build`,
# context = operator/). A CatalogSource points at the pushed image; OLM reads the
# package/channel/bundle graph from /configs.
# opm version is single-sourced in hack/versions.env; `make catalog-build` passes it as a
# --build-arg. The default here keeps a bare `docker build` working and must match it.
ARG OPM_VERSION=v1.51.0
FROM quay.io/operator-framework/opm:${OPM_VERSION}
ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs", "--cache-dir=/tmp/cache"]
ADD catalog /configs
# Pre-build the serve cache at image-build time; without it `opm serve` fails its
# startup integrity check (empty /tmp/cache → missing pogreb digest).
RUN ["/bin/opm", "serve", "/configs", "--cache-dir=/tmp/cache", "--cache-only"]
LABEL operators.operatorframework.io.index.configs.v1=/configs
