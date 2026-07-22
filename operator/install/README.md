# Install (OLM)

OLM installs the operator; one `Dotvirt` resource provisions the rest.
ArgoCD and KubeVirt must already exist; the operator waits for both.
Run everything from the repo root.

## Release

```sh
kubectl apply -f operator/install/namespace.yaml
kubectl apply -f operator/install/operatorgroup.yaml
kubectl apply -f operator/install/catalogsource.yaml
kubectl apply -f operator/install/subscription.yaml
kubectl -n dotvirt-operator get csv -w        # wait for Succeeded
kubectl create namespace dotvirt
kubectl apply -f operator/config/samples/dotvirt_v1alpha1_dotvirt.yaml   # fix the example.com hosts first
```

Upgrade: each release repins `catalogsource.yaml`; re-apply it and OLM rolls the rest.

## Dev branch (preview)

Needs quay.io/epheo push access and the pinned tools (`hack/versions.env`).

```sh
VERSION=0.0.26-rc.1 hack/preview.sh
kubectl apply -f operator/install/catalogsource-preview.yaml
```

Previews land in `candidate-v0` only; set `channel: candidate-v0` in the Subscription.
To leave a preview: delete the CSV, recreate the Subscription; OLM resolves the channel head.
