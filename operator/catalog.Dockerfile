# Serves the File-Based Catalog under catalog/ (context = operator/; built by
# hack/release.sh, hack/preview.sh and CI bundle-test). A CatalogSource points at the
# pushed image; OLM reads the package/channel/bundle graph from /configs.
# The digest is what builds; the tag must equal OPM_VERSION in hack/versions.env (CI
# validate checks it), so the server matches the opm that rendered the catalog.
FROM quay.io/operator-framework/opm:v1.74.0@sha256:b32d3891616662620da08d7f0ec42c2e69fa2de43427dc975d35b12f7a969a0f
ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs", "--cache-dir=/tmp/cache"]
ADD catalog /configs
# Pre-build the serve cache at image-build time; without it `opm serve` fails its
# startup integrity check (empty /tmp/cache → missing pogreb digest).
RUN ["/bin/opm", "serve", "/configs", "--cache-dir=/tmp/cache", "--cache-only"]
LABEL operators.operatorframework.io.index.configs.v1=/configs
