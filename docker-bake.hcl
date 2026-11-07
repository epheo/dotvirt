# The two node-run images at their release arches; CI image-build bakes both on one
# builder. Local: `docker buildx bake --set '*.platform=linux/amd64'`.
group "default" {
  targets = ["app", "operator"]
}

target "app" {
  dockerfile = "Containerfile"
  platforms  = ["linux/amd64", "linux/arm64"]
}

target "operator" {
  dockerfile = "operator/Dockerfile"
  platforms  = ["linux/amd64", "linux/arm64"]
}
