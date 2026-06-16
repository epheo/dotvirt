# Build from the REPO ROOT so both modules are in context (the operator imports the
# app module via the replace directive):
#   docker build -f operator/Dockerfile -t registry.desku.be/dotvirt-operator:<tag> .
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN cd operator && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -trimpath -o /out/manager ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
